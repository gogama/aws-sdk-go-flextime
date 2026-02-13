// Copyright 2021 The flextime Authors. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
Package flextime provides adaptive timeouts when retrying AWS SDK v2
requests.

Out of the box, the AWS SDK for Go only supports the static timeout
values available on the Go standard HTTP client (http.Client from
package net/http). These timeout values cannot be changed per request
attempt (initial attempt and retry). Install a flextime timeout
function on an AWS SDK session or client to vary timeouts across request
attempts.

To set an initial low timeout, and back off to successively higher
timeout values, use a sequence:

	f := flextime.Sequence(350*time.Millisecond, 1*time.Second, 2*time.Second)
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		// Handle error
	}
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
		// Reset counter at the start of each API call
		err := stack.Initialize.Add(&resetMiddleware{
			counter: &attemptCounter,
		}, middleware.Before)
		if err != nil {
			return err
		}
		return stack.Deserialize.Add(&timeoutMiddleware{
			timeoutFunc: f,
			counter:     &attemptCounter,
		}, middleware.Before)
	}

	cfg.APIOptions = append(cfg.APIOptions, middlewareFunc)
}

type resetMiddleware struct {
	counter *int
}

func (m *resetMiddleware) ID() string {
	return resetMiddlewareName
}

func (m *resetMiddleware) HandleInitialize(
	ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler,
) (middleware.InitializeOutput, middleware.Metadata, error) {
	*m.counter = 0
	return next.HandleInitialize(ctx, in)
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
	handlerName         = "flextime.SendHandler"
	middlewareName      = "flextime.DeserializeMiddleware"
	resetMiddlewareName = "flextime.ResetMiddleware"
	nilTimeoutFuncMsg   = "flextime: nil timeout func"
	nilWrappedFuncMsg   = "flextime: nil wrapped func"
	failedInstallMsg    = "flextime: failed swap send handler"
)

// FlextimeTimeoutError wraps an error caused by a per-attempt flextime
// timeout. It implements Timeout() bool so the SDK's standard retry
// handler (retry.IsErrorTimeout) recognises it as retryable without
// needing a custom retryer.
type FlextimeTimeoutError struct {
	Err error
}

func (e *FlextimeTimeoutError) Error() string       { return e.Err.Error() }
func (e *FlextimeTimeoutError) Unwrap() error       { return e.Err }
func (e *FlextimeTimeoutError) Timeout() bool       { return true }
func (e *FlextimeTimeoutError) CanceledError() bool { return false }

func (m *timeoutMiddleware) HandleDeserialize(
	ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler,
) (middleware.DeserializeOutput, middleware.Metadata, error) {
	// Get current attempt number
	attempt := *m.counter
	*m.counter++

	// Keep a reference to the parent (caller's) context so we can
	// distinguish a per-attempt timeout from a caller cancellation.
	parentCtx := ctx

	// Calculate timeout
	timeout := m.timeoutFunc(attempt)
	if timeout > 0 {
		// Create new context with timeout
		timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = timeoutCtx
	}

	output, metadata, err := next.HandleDeserialize(ctx, in)

	// If the child (per-attempt) context timed out but the parent
	// context is still alive, wrap the error so the SDK treats it as
	// a retryable timeout rather than a caller cancellation.
	if err != nil && ctx.Err() != nil && parentCtx.Err() == nil {
		err = &FlextimeTimeoutError{Err: err}
	}

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
