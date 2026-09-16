// Package aws implements daemon.Notifier against a user-owned AWS Lambda
// function or Step Functions state machine, invoked when daemon detects a
// Spot interruption/rebalance signal. surviva does none of the downstream
// logic here -- it hands off a JSON payload and lets the user's own
// Lambda/Step Function do whatever they want with it. See docs/specs/daemon.md.
package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
)

// LambdaNotifier invokes a Lambda function asynchronously (fire-and-forget
// -- surviva never waits on or inspects what the function actually does).
type LambdaNotifier struct {
	client *lambda.Client
	arn    string
}

// NewLambdaNotifier returns a LambdaNotifier targeting functionARN, using
// cfg for AWS credentials (the instance's own IAM role via the normal SDK
// credential chain).
func NewLambdaNotifier(cfg aws.Config, functionARN string) *LambdaNotifier {
	return &LambdaNotifier{client: lambda.NewFromConfig(cfg), arn: functionARN}
}

// Notify implements daemon.Notifier.
func (n *LambdaNotifier) Notify(ctx context.Context, payload []byte) error {
	_, err := n.client.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(n.arn),
		InvocationType: lambdatypes.InvocationTypeEvent, // async -- don't wait for the function to run
		Payload:        payload,
	})
	if err != nil {
		return fmt.Errorf("invoke lambda %s: %w", n.arn, err)
	}
	return nil
}

// StepFunctionNotifier starts a Step Functions execution. StartExecution is
// already async by nature -- it returns once the execution has started, not
// once it finishes.
type StepFunctionNotifier struct {
	client *sfn.Client
	arn    string
}

// NewStepFunctionNotifier returns a StepFunctionNotifier targeting
// stateMachineARN, using cfg for AWS credentials.
func NewStepFunctionNotifier(cfg aws.Config, stateMachineARN string) *StepFunctionNotifier {
	return &StepFunctionNotifier{client: sfn.NewFromConfig(cfg), arn: stateMachineARN}
}

// Notify implements daemon.Notifier.
func (n *StepFunctionNotifier) Notify(ctx context.Context, payload []byte) error {
	input := string(payload)
	_, err := n.client.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: aws.String(n.arn),
		Input:           aws.String(input),
	})
	if err != nil {
		return fmt.Errorf("start step function execution %s: %w", n.arn, err)
	}
	return nil
}
