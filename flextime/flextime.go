// Copyright 2021 The flextime Authors. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
Package flextime provides adaptive timeouts when retrying AWS SDK v2
requests.

Install a flextime timeout function on an AWS SDK v2 config to vary
timeouts across request attempts.

To set an initial low timeout, and back off to successively higher
timeout values, use a sequence:

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		// Handle error
	}

	f := flextime.Sequence(350*time.Millisecond, 1*time.Second, 2*time.Second)
	flextime.OnConfig(&cfg, f)
	client := dynamodb.NewFromConfig(cfg)

To roll your own timeout function:

	func myTimeoutFunc(attempt int) time.Duration {
		return time.Duration(attempt) * 500 * time.Millisecond
	}

	flextime.OnConfig(&cfg, myTimeoutFunc)
	client := s3.NewFromConfig(cfg)
*/
package flextime

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go/middleware"
)

// A TimeoutFunc computes a timeout for an AWS SDK v2 request attempt based
// on the attempt number (starting from 0 for the initial attempt).
//
// A positive return value sets a timeout of that duration on the next
// request attempt. A zero return value means no timeout.
type TimeoutFunc func(attempt int) time.Duration

// OnConfig configures the given AWS SDK v2 config to use the
// provided TimeoutFunc for adaptive timeouts.
func OnConfig(cfg *aws.Config, f TimeoutFunc) {
	if f == nil {
		panic("flextime: nil timeout func")
	}

	// Create a shared attempt counter
	attemptCounter := 0

	// Add our middleware to the config's APIOptions
	middlewareFunc := func(stack *middleware.Stack) error {
		return stack.Deserialize.Add(&timeoutMiddleware{
			timeoutFunc: f,
			counter:     &attemptCounter,
		}, middleware.Before)
	}

	cfg.APIOptions = append(cfg.APIOptions, middlewareFunc)
}

type timeoutMiddleware struct {
	timeoutFunc TimeoutFunc
	counter     *int
}

type timeoutInitialize struct{}

func (m *timeoutInitialize) ID() string {
	return handlerName
}

func (m *timeoutMiddleware) ID() string {
	return middlewareName
}

const (
	handlerName       = "flextime.SendHandler"
	middlewareName    = "flextime.DeserializeMiddleware"
	nilTimeoutFuncMsg = "flextime: nil timeout func"
	nilWrappedFuncMsg = "flextime: nil wrapped func"
	failedInstallMsg  = "flextime: failed swap send handler"
)

func (m *timeoutMiddleware) HandleDeserialize(
	ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler,
) (middleware.DeserializeOutput, middleware.Metadata, error) {
	// Get current attempt number
	attempt := *m.counter
	*m.counter++

	// Calculate timeout
	timeout := m.timeoutFunc(attempt)
	if timeout > 0 {
		// Create new context with timeout
		timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = timeoutCtx
	}

	output, metadata, err := next.HandleDeserialize(ctx, in)

	return output, metadata, err
}

// Sequence constructs a timeout function that varies the next timeout
// value if the previous attempt timed out.
//
// Use Sequence if you find the remote service often exhibits one-off
// slow response times that can be cured by quickly timing out and
// retrying, but you also need to protect your application (and the
// remote service) from retry storms and failure if the remote service
// goes through a burst of slowness where most response times during the
// burst are slower than your usual quick timeout.
//
// Parameter usual represents the timeout value the function will return
// for an initial attempt and for any retry where the immediately
// preceding attempt did not time out.
//
// Parameter after contains timeout values the function will return if
// the previous attempt timed out. If this was the first timeout of the
// execution, after[0] is returned; if the second, after[1], and so on.
// If more attempts have timed out within the client request than after
// has elements, then the last element of after is returned.
//
// Consider the following timeout function:
//
//	f := Sequence(200*time.Millisecond, time.Second, 10*time.Second)
//
// The function f will use 200 milliseconds as the usual timeout but if
// the preceding attempt timed out and was the first timeout of the
// client request, it will use 1 second; and if the previous attempt
// timed out and was not the first attempt, it will use 10 seconds.
func Sequence(usual time.Duration, after ...time.Duration) TimeoutFunc {
	p := make([]time.Duration, 1, 1+len(after))
	p[0] = usual
	p = append(p, after...)

	return func(n int) time.Duration {
		i := n
		if i > len(p)-1 {
			i = len(p) - 1
		}
		return p[i]
	}
}

func timeoutFmt(timeout time.Duration) interface{} {
	if timeout > 0 {
		return timeout
	}
	return "OFF"
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	// Check for potential timeout error
	var maybeTimeout interface{ Timeout() bool }
	if errors.As(err, &maybeTimeout) {
		return maybeTimeout.Timeout()
	}

	// Check for context deadline exceeded
	contextExceeded := errors.Is(err, context.DeadlineExceeded)
	return contextExceeded
}
