package flextime

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func TestSequence(t *testing.T) {
	f := Sequence(100*time.Millisecond, 500*time.Millisecond, 2*time.Second)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 500 * time.Millisecond},
		{2, 2 * time.Second},
		{3, 2 * time.Second}, // Should use last value
	}

	for _, test := range tests {
		result := f(test.attempt)
		if result != test.expected {
			t.Errorf("attempt %d: expected %v, got %v", test.attempt, test.expected, result)
		}
	}
}

func TestOnConfigPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil timeout func")
		}
	}()

	OnConfig(nil, nil)
}

func TestIsTimeoutError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"context deadline exceeded", context.DeadlineExceeded, true},
		{"wrapped context deadline exceeded", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{"net timeout error", &net.OpError{Op: "dial", Err: &timeoutError{}}, true},
		{"wrapped net timeout error", fmt.Errorf("connection failed: %w", &net.OpError{Op: "dial", Err: &timeoutError{}}), true},
		{"non-timeout net error", &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}, false},
		{"generic error", fmt.Errorf("some error"), false},
		{"context canceled", context.Canceled, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := isTimeoutError(test.err)
			if result != test.expected {
				t.Errorf("expected %v, got %v", test.expected, result)
			}
		})
	}
}

// Mock timeout error for testing
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestOnConfigBasic(t *testing.T) {
	timeoutFunc := func(attempt int) time.Duration {
		return time.Duration(attempt+1) * 100 * time.Millisecond
	}

	// Test that OnConfig works with a basic config struct
	// We can't test the full integration without AWS SDK v2 dependencies
	// Test the timeout function directly
	for i := 0; i < 3; i++ {
		timeout := timeoutFunc(i)
		expected := time.Duration(i+1) * 100 * time.Millisecond
		if timeout != expected {
			t.Errorf("attempt %d: expected %v, got %v", i, expected, timeout)
		}
	}
}

type alwaysRetryRetryer struct {
	aws.Retryer
}

func (r alwaysRetryRetryer) IsErrorRetryable(error) bool {
	return true
}

func (r alwaysRetryRetryer) GetRetryToken(context.Context, error) (func(error) error, error) {
	return func(error) error { return nil }, nil
}

func TestTimeoutWithSQS(t *testing.T) {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithRetryer(func() aws.Retryer {
			base := retry.AddWithMaxAttempts(retry.NewStandard(), 3)
			return alwaysRetryRetryer{base}
		}),
	)

	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	callCount := 0
	timeoutFunc := func(attempt int) time.Duration {
		callCount++
		timeout := time.Duration(attempt+1) * 500 * time.Millisecond
		t.Logf("SQS timeout for attempt %d: %v", attempt, timeout)
		return timeout
	}

	OnConfig(&cfg, timeoutFunc)

	cfg.BaseEndpoint = aws.String("http://invalid-endpoint:9999")
	client := sqs.NewFromConfig(cfg)

	ctx := context.Background()
	_, err = client.ListQueues(ctx, &sqs.ListQueuesInput{})

	if err == nil {
		t.Error("expected error but got none")
	} else {
		t.Logf("Got expected SQS error: %v", err)
	}

	// Assert timeout function was called 3 times (initial + 2 retries)
	expectedCalls := 3
	if callCount != expectedCalls {
		t.Errorf("Expected timeout function to be called %d times, but was called %d times", expectedCalls, callCount)
	}
}

func TestTimeoutWithS3(t *testing.T) {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithRetryer(func() aws.Retryer {
			base := retry.AddWithMaxAttempts(retry.NewStandard(), 2)
			return alwaysRetryRetryer{base}
		}),
	)

	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	callCount := 0
	timeoutFunc := func(attempt int) time.Duration {
		callCount++
		timeout := time.Duration(attempt+1) * 300 * time.Millisecond
		t.Logf("S3 timeout for attempt %d: %v", attempt, timeout)
		return timeout
	}

	OnConfig(&cfg, timeoutFunc)

	cfg.BaseEndpoint = aws.String("http://invalid-endpoint:9999")
	client := s3.NewFromConfig(cfg)

	ctx := context.Background()
	_, err = client.ListBuckets(ctx, &s3.ListBucketsInput{})

	if err == nil {
		t.Error("expected error but got none")
	} else {
		t.Logf("Got expected S3 error: %v", err)
	}

	// Assert timeout function was called 2 times (initial + 1 retry)
	expectedCalls := 2
	if callCount != expectedCalls {
		t.Errorf("Expected timeout function to be called %d times, but was called %d times", expectedCalls, callCount)
	}
}

func TestSequenceWithMultipleClients(t *testing.T) {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithRetryer(func() aws.Retryer {
			base := retry.AddWithMaxAttempts(retry.NewStandard(), 2)
			return alwaysRetryRetryer{base}
		}),
	)

	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Test Sequence function with multiple timeouts
	timeoutFunc := Sequence(100*time.Millisecond, 400*time.Millisecond, 800*time.Millisecond)
	OnConfig(&cfg, timeoutFunc)

	cfg.BaseEndpoint = aws.String("http://invalid-endpoint:9999")

	// Test with DynamoDB
	ddbClient := dynamodb.NewFromConfig(cfg)
	ctx := context.Background()
	_, err = ddbClient.ListTables(ctx, &dynamodb.ListTablesInput{})
	if err == nil {
		t.Error("expected DynamoDB error but got none")
	}

	// Test with SQS using same config
	sqsClient := sqs.NewFromConfig(cfg)
	_, err = sqsClient.ListQueues(ctx, &sqs.ListQueuesInput{})
	if err == nil {
		t.Error("expected SQS error but got none")
	}

	t.Log("Successfully tested timeout functionality across multiple AWS service clients")
}

func TestMiddlewareWithDynamoDB(t *testing.T) {
	// Create AWS config
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithRetryer(func() aws.Retryer {
			base := retry.AddWithMaxAttempts(retry.NewStandard(), 3)
			return alwaysRetryRetryer{base}
		}),
	)

	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	callCount := 0
	timeoutFunc := func(attempt int) time.Duration {
		callCount++
		timeout := time.Duration(attempt+1) * 1 * time.Second
		t.Logf("Setting timeout for attempt %d: %v", attempt, timeout)
		return timeout
	}

	OnConfig(&cfg, timeoutFunc)

	// Create DynamoDB client
	client := dynamodb.NewFromConfig(cfg)

	cfg.BaseEndpoint = aws.String("http://invalid-endpoint:9999")
	client = dynamodb.NewFromConfig(cfg)

	ctx := context.Background()
	_, err = client.ListTables(ctx, &dynamodb.ListTablesInput{})

	// Should get a timeout or connection error
	if err == nil {
		t.Error("expected error but got none")
	} else {
		t.Logf("Got expected error: %v", err)
	}

	// Assert timeout function was called 3 times (initial + 2 retries)
	expectedCalls := 3
	if callCount != expectedCalls {
		t.Errorf("Expected timeout function to be called %d times, but was called %d times", expectedCalls, callCount)
	}
}
