// Copyright 2021 The flextime Authors. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package flextime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
)

func TestOnConfig(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		assert.Panics(t, func() { OnConfig(nil, Sequence(time.Duration(0))) })
	})
	t.Run("nil function", func(t *testing.T) {
		cfg := aws.Config{}
		assert.Panics(t, func() { OnConfig(&cfg, nil) })
	})
	t.Run("normal case", func(t *testing.T) {
		cfg := aws.Config{}
		f := Sequence(1, 2, 3)
		OnConfig(&cfg, f)
	})
}

func TestSequence(t *testing.T) {
	Sequence(0)
	Sequence(0, 0)
	Sequence(0, 0, 0)
	f := Sequence(time.Second)
	assert.Equal(t, time.Second, f(0))
	assert.Equal(t, time.Second, f(1))
	assert.Equal(t, time.Second, f(2))
	g := Sequence(300*time.Millisecond, time.Second)
	assert.Equal(t, 300*time.Millisecond, g(0))
	assert.Equal(t, time.Second, g(1))
	assert.Equal(t, time.Second, g(2))
	h := Sequence(300*time.Millisecond, 1*time.Second, 2*time.Second)
	assert.Equal(t, 300*time.Millisecond, h(0))
	assert.Equal(t, 1*time.Second, h(1))
	assert.Equal(t, 2*time.Second, h(2))
	assert.Equal(t, 2*time.Second, h(3))
	assert.Equal(t, 2*time.Second, h(4))
}

func TestTimeoutFmt(t *testing.T) {
	t.Run("negative", func(t *testing.T) {
		x := timeoutFmt(time.Duration(-1))
		assert.Equal(t, "OFF", x)
	})
	t.Run("zero", func(t *testing.T) {
		x := timeoutFmt(time.Duration(0))
		assert.Equal(t, "OFF", x)
	})
	t.Run("positive", func(t *testing.T) {
		x := timeoutFmt(time.Duration(1))
		assert.Equal(t, time.Duration(1), x)
	})
}

func TestIsTimeout(t *testing.T) {
	assert.False(t, isTimeout(nil))
	assert.False(t, isTimeout(errors.New("foo")))
	assert.True(t, isTimeout(syscall.ETIMEDOUT))
	assert.True(t, isTimeout(&url.Error{Err: syscall.ETIMEDOUT}))
	assert.False(t, isTimeout(&url.Error{Err: errors.New("bar")}))
	assert.True(t, isTimeout(fmt.Errorf("%w", syscall.ETIMEDOUT)))
	assert.True(t, isTimeout(fmt.Errorf("eggs: %w", &url.Error{Err: syscall.ETIMEDOUT})))
	assert.False(t, isTimeout(fmt.Errorf("eggs: %w", errors.New("baz"))))
	assert.False(t, isTimeout(fmt.Errorf("eggs: %w", &url.Error{Err: errors.New("qux")})))
	assert.True(t, isTimeout(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &url.Error{Err: syscall.ETIMEDOUT}))))
}

func TestIntegration_Header(t *testing.T) {
	// This test group focuses on integration testing, against a live server,
	// timeout issues caused by the header not being served before the timeout
	// expires.
	testCases := []struct {
		name          string
		delay         []time.Duration
		expectTimeout bool
		minDuration   time.Duration // Mathematical minimum possible time
		maxDuration   time.Duration // Arbitrary upper bound for sanity
	}{
		{
			name:        "no timeouts",
			maxDuration: 200 * time.Millisecond,
		},
		{
			name:          "one timeout",
			delay:         []time.Duration{0, 0, 5 * time.Second},
			expectTimeout: true,
			minDuration:   50 * time.Millisecond,
			maxDuration:   500 * time.Millisecond,
		},
		{
			name:          "multiple timeouts",
			delay:         []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond},
			expectTimeout: true,
			minDuration:   450 * time.Millisecond,
			maxDuration:   1000 * time.Millisecond,
		},
	}

	for _, outerTestCase := range integrationTestCases {
		f := Sequence(50*time.Millisecond, 100*time.Millisecond, 300*time.Millisecond)
		t.Run(outerTestCase.name, func(t *testing.T) {
			c := outerTestCase.configFactory(t, f)
			for _, innerTestCase := range testCases {
				t.Run(innerTestCase.name, func(t *testing.T) {
					h := &simpleHandler{
						headerDelay: innerTestCase.delay,
					}
					retryer := &simpleRetryer{m: 3, n: 0}
					r := func() aws.Retryer {
						return retryer
					}
					server, client := startServer(h, *c, r)
					defer server.Close()
					outerTestCase.clientHook(t, f, client)
					start := time.Now()
					_, err := client.GetItem(context.TODO(), getItemInput)
					duration := time.Now().Sub(start)
					assert.Error(t, err)
					if innerTestCase.expectTimeout {
						assert.True(t, isTimeout(err))
					} else {
						assert.False(t, isTimeout(err))
					}
					assert.Equal(t, retryer.m, retryer.n+1)
					assert.GreaterOrEqual(t, duration, innerTestCase.minDuration)
					assert.LessOrEqual(t, duration, innerTestCase.maxDuration)
				})
			}
		})
	}
}

func TestIntegration_Body(t *testing.T) {
	// This test group focuses on integration testing, against a live server,
	// timeout issues caused by the body not being served before the timeout
	// expires.
	//
	// Note that because the flextime event handler has no way to "hook"
	// timeouts occurring during the body read, there is no point setting any
	// adaptive timeouts since they will never be used.
	testCases := []struct {
		name          string
		bodyDelay     []time.Duration
		serviceTime   []time.Duration
		expectTimeout bool
		minDuration   time.Duration // Mathematical minimum possible time
		maxDuration   time.Duration // Arbitrary upper bound for sanity
	}{
		{
			name:        "no timeout",
			serviceTime: []time.Duration{10 * time.Microsecond, 5 * time.Microsecond},
			minDuration: 15 * time.Microsecond,
			maxDuration: 500 * time.Millisecond,
		},
		{
			name:          "timeout",
			bodyDelay:     []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 400 * time.Millisecond, 400 * time.Millisecond, 400 * time.Millisecond},
			serviceTime:   []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 1 * time.Second, 1 * time.Second, 1 * time.Second},
			expectTimeout: true,
			minDuration:   300 * time.Millisecond,
			maxDuration:   750 * time.Second,
		},
	}

	for _, outerTestCase := range integrationTestCases {
		f := Sequence(100 * time.Millisecond)
		t.Run(outerTestCase.name, func(t *testing.T) {
			c := outerTestCase.configFactory(t, f)
			for _, innerTestCase := range testCases {
				t.Run(innerTestCase.name, func(t *testing.T) {
					h := &simpleHandler{
						bodyDelay:       innerTestCase.bodyDelay,
						bodyServiceTime: innerTestCase.serviceTime,
					}
					retryer := &simpleRetryer{m: 5, n: 0}
					r := func() aws.Retryer {
						return retryer
					}
					server, client := startServer(h, *c, r)
					defer server.Close()
					outerTestCase.clientHook(t, f, client)
					start := time.Now()
					_, err := client.GetItem(context.TODO(), getItemInput)
					duration := time.Now().Sub(start)
					assert.Error(t, err)
					if innerTestCase.expectTimeout {
						assert.True(t, isTimeout(err))
					} else {
						assert.False(t, isTimeout(err))
					}
					assert.Equal(t, retryer.m, retryer.n+1)
					assert.GreaterOrEqual(t, duration, innerTestCase.minDuration)
					assert.LessOrEqual(t, duration, innerTestCase.maxDuration)
				})
			}
		})
	}
}

type simpleRetryer struct {
	m, n int
}

func (r *simpleRetryer) MaxAttempts() int {
	return r.m
}

func (r *simpleRetryer) RetryDelay(attempt int, err error) (time.Duration, error) {
	r.n++
	return 0, nil
}

func (r *simpleRetryer) IsErrorRetryable(err error) bool {
	return true
}

func (r *simpleRetryer) GetRetryToken(ctx context.Context, opErr error) (func(error) error, error) {
	return func(error) error { return nil }, nil
}

func (r *simpleRetryer) GetInitialToken() func(error) error {
	return func(error) error { return nil }
}

type simpleHandler struct {
	headerDelay     []time.Duration
	bodyDelay       []time.Duration
	bodyServiceTime []time.Duration
	i               int
}

// const body =

func (h *simpleHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	defer func() { h.i++ }()

	// Parley the response writer into a flusher. We use the flusher so we can
	// flush work in progress down to the network stack as soon as it is ready,
	// to ensure that the client sees a steady stream of data rather than a big
	// bang at the end.
	f, ok := w.(http.Flusher)
	if !ok {
		panic("w is not a Flusher")
	}

	// Define the static body bytes we will return.
	body := []byte(`	{

		"message": "Something went terribly wrong with the thing we were trying to do here."

	}`)

	// Pause for the delay time specified before writing the header.
	h.pause(h.headerDelay)

	// Write the header, along with a content-length field so the client knows
	// how much data to expect.
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(500)
	f.Flush()

	// Pause for the delay time specified before writing the body.
	h.pause(h.bodyDelay)

	// Write the body one byte at a time, pausing and flushing between each byte
	// and overall ensuring that the time taken to write the entire body takes
	// at least the specified service duration.
	var bodyServiceDuration, bytePause time.Duration
	bodyStart := time.Now()
	if h.i < len(h.bodyServiceTime) {
		bodyServiceDuration = h.bodyServiceTime[h.i]
		bytePause = bodyServiceDuration / time.Duration(len(body))
	}
	for i := 0; i < len(body)-1; i++ {
		time.Sleep(bytePause)
		_, err := w.Write(body[i : i+1])
		if err != nil {
			return
		}
		f.Flush()
	}
	time.Sleep(bodyServiceDuration - time.Now().Sub(bodyStart))
	_, _ = w.Write(body[len(body)-1:])
}

var integrationTestCases = []struct {
	name          string
	configFactory func(t *testing.T, f TimeoutFunc) *aws.Config
	clientHook    func(t *testing.T, f TimeoutFunc, c *dynamodb.Client)
}{
	{
		name: "OnConfig",
		configFactory: func(t *testing.T, f TimeoutFunc) *aws.Config {
			c, _ := config.LoadDefaultConfig(context.TODO(),
				config.WithRegion("us-east-1"),
				config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
			)
			OnConfig(&c, f)
			return &c
		},
		clientHook: func(_ *testing.T, _ TimeoutFunc, _ *dynamodb.Client) {},
	},
}

func (h *simpleHandler) pause(delay []time.Duration) {
	if h.i < len(delay) {
		d := delay[h.i]
		time.Sleep(d)
	}
}

func startServer(h http.Handler, cfg aws.Config, r func() aws.Retryer) (*httptest.Server, *dynamodb.Client) {
	server := httptest.NewTLSServer(h)
	cfg.BaseEndpoint = aws.String(server.URL)
	cfg.Retryer = r
	cfg.HTTPClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	client := dynamodb.NewFromConfig(cfg)
	return server, client
}

var getItemInput = &dynamodb.GetItemInput{
	TableName: aws.String("foo"),
	Key: map[string]types.AttributeValue{
		"bar": &types.AttributeValueMemberS{
			Value: "baz",
		},
	},
}
