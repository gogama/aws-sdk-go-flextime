// Copyright 2021 The flextime Authors. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package flextime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"

	"github.com/aws/aws-sdk-go/service/sqs"

	"github.com/aws/aws-sdk-go/aws/awserr"

	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go/aws/credentials"

	"github.com/aws/aws-sdk-go/service/dynamodb"

	"github.com/aws/aws-sdk-go/aws/session"

	"github.com/aws/aws-sdk-go/aws/client/metadata"

	"github.com/stretchr/testify/mock"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"

	"github.com/stretchr/testify/assert"
)

func TestOnSession(t *testing.T) {
	t.Run("nil session", func(t *testing.T) {
		assert.Panics(t, func() { _ = OnSession(nil, Sequence(time.Duration(0))) })
	})
	t.Run("nil function", func(t *testing.T) {
		s := session.Must(session.NewSession())
		assert.Panics(t, func() { _ = OnSession(s, nil) })
	})
	t.Run("unable to swap ", func(t *testing.T) {
		t.Run("missing core.SendHandler", func(t *testing.T) {
			s := session.Must(session.NewSession())
			s.Handlers.Send.RemoveByName(coreSendHandler.Name)
			err := OnSession(s, Sequence(1, 2, 3))
			assert.EqualError(t, err, failedInstallMsg)
		})
		t.Run("missing flextime.SendHandler", func(t *testing.T) {
			s := session.Must(session.NewSession())
			err := OnSession(s, Sequence(1, 2, 3))
			assert.NoError(t, err)
			err = OnSession(s, Sequence(4))
			assert.NoError(t, err)
			s.Handlers.Send.RemoveByName(handlerName)
			err = OnSession(s, Sequence(5, 6))
			assert.EqualError(t, err, failedInstallMsg)
		})
	})
	t.Run("normal case", func(t *testing.T) {
		s := session.Must(session.NewSession())
		f := Sequence(1, 2, 3)
		err := OnSession(s, f)
		assert.NoError(t, err)
	})
}

func TestOnClient(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		assert.Panics(t, func() { _ = OnClient(nil, Sequence(time.Duration(0))) })
	})
	t.Run("nil function", func(t *testing.T) {
		s := session.Must(session.NewSession())
		ddb := dynamodb.New(s)
		assert.Panics(t, func() { _ = OnClient(ddb.Client, nil) })
	})
	t.Run("unable to swap ", func(t *testing.T) {
		s := session.Must(session.NewSession())
		t.Run("missing core.SendHandler", func(t *testing.T) {
			client := sqs.New(s)
			client.Handlers.Send.RemoveByName(coreSendHandler.Name)
			err := OnClient(client.Client, Sequence(1, 2, 3))
			assert.EqualError(t, err, failedInstallMsg)
		})
		t.Run("missing flextime.SendHandler", func(t *testing.T) {
			client := s3.New(s)
			err := OnClient(client.Client, Sequence(1))
			assert.NoError(t, err)
			err = OnClient(client.Client, Sequence(2, 3, 4, 5, 6, 7))
			assert.NoError(t, err)
			client.Handlers.Send.RemoveByName(handlerName)
			err = OnClient(client.Client, Sequence(8, 9, 10, 11, 12, 13))
			assert.EqualError(t, err, failedInstallMsg)
		})
	})
	t.Run("normal case", func(t *testing.T) {
		s := session.Must(session.NewSession())
		ddb := dynamodb.New(s)
		f := Sequence(1, 2, 3)
		err := OnClient(ddb.Client, f)
		assert.NoError(t, err)
	})
}

func TestSequence(t *testing.T) {
	Sequence(0)
	Sequence(0, 0)
	Sequence(0, 0, 0)
	f := Sequence(time.Second)
	assert.Equal(t, time.Second, f(newTestRequest(), 0))
	assert.Equal(t, time.Second, f(newTestRequest(), 1))
	assert.Equal(t, time.Second, f(newTestRequest(), 2))
	g := Sequence(300*time.Millisecond, time.Second)
	assert.Equal(t, 300*time.Millisecond, g(newTestRequest(), 0))
	assert.Equal(t, time.Second, g(newTestRequest(), 1))
	assert.Equal(t, time.Second, g(newTestRequest(), 2))
	h := Sequence(300*time.Millisecond, 1*time.Second, 2*time.Second)
	assert.Equal(t, 300*time.Millisecond, h(newTestRequest(), 0))
	assert.Equal(t, 1*time.Second, h(newTestRequest(), 1))
	assert.Equal(t, 2*time.Second, h(newTestRequest(), 2))
	assert.Equal(t, 2*time.Second, h(newTestRequest(), 3))
	assert.Equal(t, 2*time.Second, h(newTestRequest(), 4))
}

func TestWrapWithTimeout(t *testing.T) {
	t.Run("construct closure nil timeout func", func(t *testing.T) {
		assert.PanicsWithValue(t, nilTimeoutFuncMsg, func() { wrapWithTimeout(nil, wrappableFunc()) })
	})
	t.Run("construct closure nil wrapped func", func(t *testing.T) {
		assert.PanicsWithValue(t, nilWrappedFuncMsg, func() { wrapWithTimeout(Sequence(1), nil) })
	})
	t.Run("closure", func(t *testing.T) {
		const (
			configInContext = 0x01
			debugLoggingOn  = 0x02
			loggerAvailable = 0x04
			timeoutGTZero   = 0x08
			timeoutErr      = 0x10
			nonTimeoutErr   = 0x20
			allOptions      = configInContext | debugLoggingOn | loggerAvailable | timeoutGTZero | timeoutErr | nonTimeoutErr
		)
		for i := 0; i <= allOptions; i++ {
			// Construct test case names.
			var names []string
			if i&configInContext == configInContext {
				names = append(names, "config")
			}
			if i&debugLoggingOn == debugLoggingOn {
				names = append(names, "debug")
			}
			if i&loggerAvailable == loggerAvailable {
				names = append(names, "logger")
			}
			if i&timeoutGTZero == timeoutGTZero {
				names = append(names, "timeout gt 0")
			}
			if i&timeoutErr == timeoutErr {
				names = append(names, "timeout error")
			} else if i&nonTimeoutErr == nonTimeoutErr {
				names = append(names, "non-timeout error")
			}

			name := strings.Join(names, " ")
			if name == "" {
				name = "none"
			}

			t.Run(name, func(t *testing.T) {
				// Setup test case.
				r := newTestRequest()
				var cfg *config
				if i&configInContext == configInContext {
					cfg = &config{n: 101}
					r.SetContext(context.WithValue(r.Context(), configKey, cfg))
				}
				if i&debugLoggingOn == debugLoggingOn {
					r.Config.LogLevel = aws.LogLevel(aws.LogDebug)
				}
				var l *mockLogger
				if i&loggerAvailable == loggerAvailable {
					l = &mockLogger{}
					l.Test(t)
					r.Config.Logger = l
				}
				var tf TimeoutFunc
				var r2 *request.Request
				var n2 int
				if i&timeoutGTZero == timeoutGTZero {
					tf = func(r *request.Request, n int) time.Duration {
						r2 = r
						n2 = n
						return time.Minute
					}
					if l != nil && i&debugLoggingOn == debugLoggingOn {
						l.On("Log", []interface{}{"DEBUG: flextime test/test timeout 1m0s"}).Return().Once()
					}
				} else {
					tf = func(r *request.Request, n int) time.Duration {
						r2 = r
						n2 = n
						return 0
					}
					if l != nil && i&debugLoggingOn == debugLoggingOn {
						l.On("Log", []interface{}{"DEBUG: flextime test/test timeout OFF"}).Return().Once()
					}
				}
				var wrapErr error
				if i&timeoutErr == timeoutErr {
					wrapErr = syscall.ETIMEDOUT
				} else if i&nonTimeoutErr == nonTimeoutErr {
					wrapErr = errors.New("a non-timeout error of some kind")
				}
				var wrapTime time.Time
				var wrapHTTPReq *http.Request

				// Run test case.
				start := time.Now()
				b := wrapWithTimeout(tf, wrappableFunc(wrapErr, &wrapTime, &wrapHTTPReq))
				b(r)
				end := time.Now()

				// Validate expectations.
				if l != nil {
					l.AssertExpectations(t)
				}
				assert.Same(t, r, r2)
				deadline, deadlineOK := r.HTTPRequest.Context().Deadline()
				assert.False(t, deadlineOK)
				require.NotNil(t, wrapHTTPReq)
				deadline, deadlineOK = wrapHTTPReq.Context().Deadline()
				if i&timeoutGTZero == timeoutGTZero {
					assert.NotSame(t, wrapHTTPReq, r.HTTPRequest)
					assert.True(t, deadlineOK)
					assert.False(t, deadline.Before(start.Add(time.Minute)))
					assert.False(t, deadline.After(start.Add(time.Minute+5*time.Millisecond)))
				} else {
					assert.Same(t, wrapHTTPReq, r.HTTPRequest)
					assert.False(t, deadlineOK)
				}
				require.IsType(t, &config{}, r.Context().Value(configKey))
				if i&configInContext == configInContext {
					assert.Same(t, cfg, r.Context().Value(configKey))
					assert.Equal(t, 101, n2)
					if i&timeoutErr == timeoutErr {
						assert.Equal(t, 102, cfg.n)
					} else {
						assert.Equal(t, 101, cfg.n)
					}
				} else {
					assert.Nil(t, cfg)
					assert.Equal(t, 0, n2)
					cfg = r.Context().Value(configKey).(*config)
					if i&timeoutErr == timeoutErr {
						assert.Equal(t, 1, cfg.n)
					} else {
						assert.Equal(t, 0, cfg.n)
					}
				}
				assert.False(t, wrapTime.Before(start))
				assert.False(t, end.Before(wrapTime))
			})
		}
	})
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

func TestLogDebug(t *testing.T) {
	t.Run("nil logger", func(t *testing.T) {
		r := &request.Request{
			Config: aws.Config{
				LogLevel: aws.LogLevel(aws.LogDebug),
			},
		}
		assert.NotPanics(t, func() { logDebug(r, "foo") })
	})
	t.Run("logging off", func(t *testing.T) {
		l := &mockLogger{}
		r := &request.Request{
			Config: aws.Config{
				LogLevel: aws.LogLevel(aws.LogOff),
				Logger:   l,
			},
		}
		logDebug(r, "foo %s", "bar")
		l.AssertNotCalled(t, "Log", mock.Anything)
	})
	t.Run("logging on", func(t *testing.T) {
		r := &request.Request{
			Config: aws.Config{
				LogLevel: aws.LogLevel(aws.LogDebug),
			},
			ClientInfo: metadata.ClientInfo{
				ServiceName: "ham",
			},
			Operation: &request.Operation{
				Name: "eggs",
			},
		}
		t.Run("no args", func(t *testing.T) {
			l := &mockLogger{}
			r.Config.Logger = l
			l.On("Log", []interface{}{"DEBUG: flextime ham/eggs baz"})
			logDebug(r, "baz")
			l.AssertExpectations(t)
		})
		t.Run("with args", func(t *testing.T) {
			l := &mockLogger{}
			r.Config.Logger = l
			l.On("Log", []interface{}{"DEBUG: flextime ham/eggs 1 2 3"})
			logDebug(r, "%d %d %d", 1, 2, 3)
			l.AssertExpectations(t)
		})
	})
}

func TestIsTimeout(t *testing.T) {
	assert.False(t, isTimeout(nil))
	assert.False(t, isTimeout(errors.New("foo")))
	assert.True(t, isTimeout(syscall.ETIMEDOUT))
	assert.True(t, isTimeout(&url.Error{Err: syscall.ETIMEDOUT}))
	assert.False(t, isTimeout(&url.Error{Err: errors.New("bar")}))
	assert.True(t, isTimeout(awserr.New("ham", "eggs", syscall.ETIMEDOUT)))
	assert.True(t, isTimeout(awserr.New("ham", "eggs", &url.Error{Err: syscall.ETIMEDOUT})))
	assert.False(t, isTimeout(awserr.New("ham", "eggs", errors.New("baz"))))
	assert.False(t, isTimeout(awserr.New("ham", "eggs", &url.Error{Err: errors.New("qux")})))
	assert.True(t, isTimeout(awserr.New("outer", "outer", awserr.New("inner", "inner", &url.Error{Err: syscall.ETIMEDOUT}))))
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
			maxDuration: 100 * time.Millisecond,
		},
		{
			name:          "one timeout",
			delay:         []time.Duration{0, 0, 100 * time.Millisecond},
			expectTimeout: true,
			minDuration:   50 * time.Millisecond,
			maxDuration:   200 * time.Millisecond,
		},
		{
			name:          "multiple timeouts",
			delay:         []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond},
			expectTimeout: true,
			minDuration:   450 * time.Millisecond,
			maxDuration:   800 * time.Millisecond,
		},
	}

	for _, outerTestCase := range integrationTestCases {
		f := Sequence(50*time.Millisecond, 100*time.Millisecond, 300*time.Millisecond)
		t.Run(outerTestCase.name, func(t *testing.T) {
			s := outerTestCase.sessionFactory(t, f)
			for _, innerTestCase := range testCases {
				t.Run(innerTestCase.name, func(t *testing.T) {
					h := &simpleHandler{
						headerDelay: innerTestCase.delay,
					}
					r := &simpleRetryer{m: 3}
					server, client := startServer(s, h, r)
					defer server.Close()
					outerTestCase.clientHook(t, f, client)
					start := time.Now()
					_, err := client.GetItem(getItemInput)
					duration := time.Now().Sub(start)
					assert.Error(t, err)
					if innerTestCase.expectTimeout {
						assert.True(t, isTimeout(err))
					} else {
						assert.False(t, isTimeout(err))
					}
					assert.Equal(t, 3, r.n)
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
			s := outerTestCase.sessionFactory(t, f)
			for _, innerTestCase := range testCases {
				t.Run(innerTestCase.name, func(t *testing.T) {
					h := &simpleHandler{
						bodyDelay:       innerTestCase.bodyDelay,
						bodyServiceTime: innerTestCase.serviceTime,
					}
					r := &simpleRetryer{m: 5}
					server, client := startServer(s, h, r)
					defer server.Close()
					outerTestCase.clientHook(t, f, client)
					start := time.Now()
					_, err := client.GetItem(getItemInput)
					duration := time.Now().Sub(start)
					assert.Error(t, err)
					if innerTestCase.expectTimeout {
						assert.True(t, isTimeout(err))
					} else {
						assert.False(t, isTimeout(err))
					}
					assert.Equal(t, r.m, r.n)
					assert.GreaterOrEqual(t, duration, innerTestCase.minDuration)
					assert.LessOrEqual(t, duration, innerTestCase.maxDuration)
				})
			}
		})
	}
}

// Constructor for a simple SDK-compatible request handler that is useful
// for testing. If you pass it a pointer to a time.Time, it will set it
// to point to the current time when invoked. If you pass it an error,
// it will set the request error to that value when invoked.
func wrappableFunc(i ...interface{}) func(*request.Request) {
	j := make([]interface{}, 0, len(i))
	j = append(j, i...)
	return func(r *request.Request) {
		now := time.Now()
		for _, x := range j {
			if err, ok := x.(error); ok {
				r.Error = err
				continue
			}
			if req, ok := x.(**http.Request); ok {
				*req = r.HTTPRequest
				continue
			}
			if pt, ok := x.(*time.Time); ok {
				*pt = now
				continue
			}
		}
	}
}

type mockLogger struct {
	mock.Mock
}

func (m *mockLogger) Log(a ...interface{}) {
	m.Called(a)
}

func newTestRequest() *request.Request {
	return request.New(aws.Config{}, metadata.ClientInfo{ServiceName: "test"}, request.Handlers{}, nil, testOp, nil, nil)
}

var testOp = &request.Operation{
	Name: "test",
}

type simpleRetryer struct {
	m, n int
}

func (r *simpleRetryer) MaxRetries() int {
	return r.m
}

func (r *simpleRetryer) RetryRules(_ *request.Request) time.Duration {
	r.n++
	return 0
}

func (r *simpleRetryer) ShouldRetry(_ *request.Request) bool {
	return true
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
	name           string
	sessionFactory func(t *testing.T, f TimeoutFunc) *session.Session
	clientHook     func(t *testing.T, f TimeoutFunc, c *dynamodb.DynamoDB)
}{
	{
		name: "OnSession",
		sessionFactory: func(t *testing.T, f TimeoutFunc) *session.Session {
			s, err := session.NewSession()
			require.NoError(t, err)
			err = OnSession(s, f)
			require.NoError(t, err)
			return s
		},
		clientHook: func(_ *testing.T, _ TimeoutFunc, _ *dynamodb.DynamoDB) {},
	},
	{
		name: "OnClient",
		sessionFactory: func(t *testing.T, _ TimeoutFunc) *session.Session {
			s, err := session.NewSession()
			require.NoError(t, err)
			return s
		},
		clientHook: func(t *testing.T, f TimeoutFunc, c *dynamodb.DynamoDB) {
			err := OnClient(c.Client, f)
			require.NoError(t, err)
		},
	},
}

func (h *simpleHandler) pause(delay []time.Duration) {
	if h.i < len(delay) {
		d := delay[h.i]
		time.Sleep(d)
	}
}

func startServer(s *session.Session, h http.Handler, r *simpleRetryer) (*httptest.Server, *dynamodb.DynamoDB) {
	server := httptest.NewTLSServer(h)
	ddb := dynamodb.New(s, &aws.Config{
		Credentials: credentials.AnonymousCredentials,
		Region:      aws.String("test-region"),
		Endpoint:    aws.String(server.URL),
		HTTPClient:  server.Client(),
		Retryer:     r,
	})
	return server, ddb
}

var getItemInput = &dynamodb.GetItemInput{
	TableName: aws.String("foo"),
	Key: map[string]*dynamodb.AttributeValue{
		"bar": {
			S: aws.String("baz"),
		},
	},
}
