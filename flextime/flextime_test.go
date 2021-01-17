package flextime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

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
		assert.Panics(t, func() { OnSession(nil, Sequence(time.Duration(0))) })
	})
	t.Run("nil function", func(t *testing.T) {
		s := session.Must(session.NewSession())
		assert.Panics(t, func() { OnSession(s, nil) })
	})
	t.Run("normal case", func(t *testing.T) {
		s := session.Must(session.NewSession())
		f := Sequence(1, 2, 3)
		OnSession(s, f)
	})
}

func TestOnClient(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		assert.Panics(t, func() { OnClient(nil, Sequence(time.Duration(0))) })
	})
	t.Run("nil function", func(t *testing.T) {
		s := session.Must(session.NewSession())
		ddb := dynamodb.New(s)
		assert.Panics(t, func() { OnClient(ddb.Client, nil) })
	})
	t.Run("normal case", func(t *testing.T) {
		s := session.Must(session.NewSession())
		ddb := dynamodb.New(s)
		f := Sequence(1, 2, 3)
		OnClient(ddb.Client, f)
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

func TestBeforeSend(t *testing.T) {
	t.Run("construct closure nil timeout func", func(t *testing.T) {
		assert.PanicsWithValue(t, nilTimeoutFuncMsg, func() { beforeSend(nil) })
	})
	t.Run("closure", func(t *testing.T) {
		const (
			configInContext = 0x1
			debugLoggingOn  = 0x2
			loggerAvailable = 0x4
			timeoutGTZero   = 0x8
			allOptions      = configInContext | debugLoggingOn | loggerAvailable | timeoutGTZero
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
				var f TimeoutFunc
				var r2 *request.Request
				var n2 int
				if i&timeoutGTZero == timeoutGTZero {
					f = func(r *request.Request, n int) time.Duration {
						r2 = r
						n2 = n
						return time.Minute
					}
					if l != nil && i&debugLoggingOn == debugLoggingOn {
						l.On("Log", []interface{}{"DEBUG: flextime test/test timeout 1m0s"}).Return().Once()
					}
				} else {
					f = func(r *request.Request, n int) time.Duration {
						r2 = r
						n2 = n
						return 0
					}
					if l != nil && i&debugLoggingOn == debugLoggingOn {
						l.On("Log", []interface{}{"DEBUG: flextime test/test timeout OFF"}).Return().Once()
					}
				}

				// Run test case.
				start := time.Now()
				b := beforeSend(f)
				b.Fn(r)

				// Validate expectations.
				if l != nil {
					l.AssertExpectations(t)
				}
				assert.Same(t, r, r2)
				deadline, deadlineOK := r.HTTPRequest.Context().Deadline()
				if i&timeoutGTZero == timeoutGTZero {
					assert.True(t, deadlineOK)
					assert.False(t, deadline.Before(start.Add(time.Minute)))
					assert.False(t, deadline.After(start.Add(time.Minute+100*time.Microsecond)))
				} else {
					assert.False(t, deadlineOK)
				}
				assert.IsType(t, &config{}, r.Context().Value(configKey))
				if i&configInContext == configInContext {
					assert.Equal(t, 101, n2)
				} else {
					assert.Equal(t, 0, n2)
				}
			})
		}
	})
}

func TestAfterSend(t *testing.T) {
	t.Run("missing config", func(t *testing.T) {
		r := newTestRequest()
		assert.PanicsWithValue(t, "flextime: missing config", func() { afterSend.Fn(r) })
	})
	t.Run("wrong config type", func(t *testing.T) {
		r := newTestRequest()
		r.SetContext(context.WithValue(r.Context(), configKey, "foo"))
		assert.PanicsWithValue(t, "flextime: wrong config type", func() { afterSend.Fn(r) })
	})
	t.Run("nil cancel func", func(t *testing.T) {
		r := newTestRequest()
		r.SetContext(context.WithValue(r.Context(), configKey, &config{}))
		assert.PanicsWithValue(t, "flextime: nil cancel func", func() { afterSend.Fn(r) })
	})
	t.Run("timeout no", func(t *testing.T) {
		var canceled bool
		config := &config{
			cancel: func() { canceled = true },
		}
		r := newTestRequest()
		r.SetContext(context.WithValue(r.Context(), configKey, config))
		r.Error = errors.New("ain't no timeout")
		afterSend.Fn(r)
		assert.True(t, canceled)
		assert.Nil(t, config.cancel)
		assert.Equal(t, 0, config.n)
	})
	t.Run("timeout yes", func(t *testing.T) {
		var canceled bool
		cancelFunc := func() { canceled = true }
		config := &config{cancel: cancelFunc}
		r := newTestRequest()
		r.SetContext(context.WithValue(r.Context(), configKey, config))
		r.Error = syscall.ETIMEDOUT

		// First call
		afterSend.Fn(r)
		assert.True(t, canceled)
		assert.Nil(t, config.cancel)
		assert.Equal(t, 1, config.n)

		// Second call
		canceled = false
		config.cancel = cancelFunc
		afterSend.Fn(r)
		assert.True(t, canceled)
		assert.Nil(t, config.cancel)
		assert.Equal(t, 2, config.n)
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

func TestIntegration(t *testing.T) {
	t.Run("OnSession", func(t *testing.T) {
		s := session.Must(session.NewSession())
		OnSession(s, Sequence(50*time.Millisecond, 500*time.Millisecond))
		t.Run("one timeout", func(t *testing.T) {
			h := &simpleHandler{
				delay: []time.Duration{0, 0, 100 * time.Millisecond},
			}
			r := &simpleRetryer{}
			server, client := startServer(s, h, r)
			defer server.Close()
			start := time.Now()
			_, err := client.GetItem(getItemInput)
			duration := time.Now().Sub(start)
			assert.EqualError(t, err, "zzz")
			assert.Error(t, err)
			assert.Equal(t, numRetries, r.n)
			assert.GreaterOrEqual(t, duration, 50*time.Millisecond)
		})
	})
	t.Run("OnClient", func(t *testing.T) {
		//s := session.Must(session.NewSession())

	})
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
	defer func() { h.i++ }()
	if h.i < len(h.delay) {
		time.Sleep(h.delay[h.i])
	}
	w.WriteHeader(500)
	w.Write([]byte(`{"message":"Something failed"}`))
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
		"bar": &dynamodb.AttributeValue{
			S: aws.String("baz"),
		},
	},
}
