// Package remote durably records checkpoint job status in DynamoDB and
// pushes checkpoint data to S3, independent of the instance that wrote
// them — so a restore orchestrator can tell whether a checkpoint is safe
// to restore even after the instance that produced it is gone.
package remote

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"surviva/internal/job"
)

// Record is one item in the DynamoDB job status table.
type Record struct {
	JobID            string    `dynamodbav:"job_id"`
	InstanceID       string    `dynamodbav:"instance_id,omitempty"`
	AZ               string    `dynamodbav:"az,omitempty"`
	Command          []string  `dynamodbav:"command"`
	Priority         int       `dynamodbav:"priority"`
	CheckpointMethod string    `dynamodbav:"checkpoint_method"` // "criu" | "hook"
	StorageType      string    `dynamodbav:"storage_type"`      // "s3" | "ebs"
	S3URI            string    `dynamodbav:"s3_uri,omitempty"`
	SizeBytes        int64     `dynamodbav:"size_bytes,omitempty"`
	Status           string    `dynamodbav:"status"`
	FailureReason    string    `dynamodbav:"failure_reason,omitempty"`
	UpdatedAt        time.Time `dynamodbav:"updated_at"`
}

// StatusStore is a DynamoDB-backed job status table.
type StatusStore struct {
	client    *dynamodb.Client
	tableName string
}

// NewStatusStore returns a StatusStore backed by tableName.
func NewStatusStore(cfg aws.Config, tableName string) *StatusStore {
	return &StatusStore{client: dynamodb.NewFromConfig(cfg), tableName: tableName}
}

// Put writes (or overwrites) a job's full status record. Callers write an
// IN_PROGRESS record with this before starting the (potentially large,
// potentially slow) upload, so that if the instance dies mid-upload the
// table is left showing IN_PROGRESS rather than nothing at all — an
// orchestrator must treat any status other than CHECKPOINT_COMPLETE as
// unsafe to restore.
func (s *StatusStore) Put(ctx context.Context, r Record) error {
	item, err := attributevalue.MarshalMap(r)
	if err != nil {
		return fmt.Errorf("marshal job record %s: %w", r.JobID, err)
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("put job record %s: %w", r.JobID, err)
	}
	return nil
}

// Complete marks a job's checkpoint as durably stored at s3URI.
func (s *StatusStore) Complete(ctx context.Context, jobID, s3URI string, sizeBytes int64) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression: aws.String("SET #status = :status, s3_uri = :s3_uri, size_bytes = :size_bytes, updated_at = :updated_at"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":status":     &types.AttributeValueMemberS{Value: string(job.StatusCheckpointComplete)},
			":s3_uri":     &types.AttributeValueMemberS{Value: s3URI},
			":size_bytes": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", sizeBytes)},
			":updated_at": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		return fmt.Errorf("mark job %s complete: %w", jobID, err)
	}
	return nil
}

// Fail marks a job's checkpoint as incomplete/unsafe to restore, recording
// why. This is best-effort: if the instance dies before this call can
// succeed, the record is simply left at whatever IN_PROGRESS state Put
// wrote, which an orchestrator treats identically (not safe to restore).
func (s *StatusStore) Fail(ctx context.Context, jobID, reason string) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression: aws.String("SET #status = :status, failure_reason = :reason, updated_at = :updated_at"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":status":     &types.AttributeValueMemberS{Value: string(job.StatusCheckpointIncomplete)},
			":reason":     &types.AttributeValueMemberS{Value: reason},
			":updated_at": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		return fmt.Errorf("mark job %s incomplete: %w", jobID, err)
	}
	return nil
}

// Get fetches one job's status record. It returns (nil, nil) if no record
// exists for jobID.
func (s *StatusStore) Get(ctx context.Context, jobID string) (*Record, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get job record %s: %w", jobID, err)
	}
	if out.Item == nil {
		return nil, nil
	}
	var r Record
	if err := attributevalue.UnmarshalMap(out.Item, &r); err != nil {
		return nil, fmt.Errorf("unmarshal job record %s: %w", jobID, err)
	}
	return &r, nil
}
