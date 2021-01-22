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
}

func TestIntegration(t *testing.T) {
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

	t.Run("OnSession", func(t *testing.T) {
		s := session.Must(session.NewSession())
		err := OnSession(s, Sequence(50*time.Millisecond, 200*time.Millisecond))
		require.NoError(t, err)
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				h := &simpleHandler{
					delay: testCase.delay,
				}
				r := &simpleRetryer{}
				server, client := startServer(s, h, r)
				defer server.Close()
				start := time.Now()
				_, err := client.GetItem(getItemInput)
				duration := time.Now().Sub(start)
				assert.Error(t, err)
				if testCase.expectTimeout {
					assert.True(t, isTimeout(err))
				} else {
					assert.False(t, isTimeout(err))
				}
				assert.Equal(t, numRetries, r.n)
				assert.GreaterOrEqual(t, duration, testCase.minDuration)
				assert.LessOrEqual(t, duration, testCase.maxDuration)
			})
		}
	})
	t.Run("OnClient", func(t *testing.T) {
		s := session.Must(session.NewSession())
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				h := &simpleHandler{
					delay: testCase.delay,
				}
				r := &simpleRetryer{}
				server, client := startServer(s, h, r)
				defer server.Close()
				err := OnClient(client.Client, Sequence(50*time.Millisecond, 100*time.Millisecond, 300*time.Millisecond))
				require.NoError(t, err)
				start := time.Now()
				_, err = client.GetItem(getItemInput)
				duration := time.Now().Sub(start)
				assert.Error(t, err)
				if testCase.expectTimeout {
					assert.True(t, isTimeout(err))
				} else {
					assert.False(t, isTimeout(err))
				}
				assert.Equal(t, numRetries, r.n)
				assert.GreaterOrEqual(t, duration, testCase.minDuration)
				assert.LessOrEqual(t, duration, testCase.maxDuration)
			})
		}
	})
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
	n int
}

const numRetries = 2

func (r *simpleRetryer) MaxRetries() int {
	return numRetries
}

func (r *simpleRetryer) RetryRules(_ *request.Request) time.Duration {
	r.n++
	return 0
}

func (r *simpleRetryer) ShouldRetry(_ *request.Request) bool {
	return true
}

type simpleHandler struct {
	delay []time.Duration
	i     int
}

func (h *simpleHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	i := h.i
	h.i++
	if i < len(h.delay) {
		time.Sleep(h.delay[i])
	}
	w.WriteHeader(500)
	_, _ = w.Write([]byte(`{"message":"Something failed"}`))
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
