package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2 is a struct describing r2 cloudflare storage bucket
type R2 struct {
	Bucket string
	client *s3.Client
}

// NewR2 creates new R2 struct
func NewR2(
	accountID string,
	accessKeyID string,
	accessKeySecret string,
	bucket string,
) (*R2, error) {
	r2Resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL: fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID),
		}, nil
	})

	cfg, err := config.LoadDefaultConfig(
		context.TODO(),
		config.WithEndpointResolverWithOptions(r2Resolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, accessKeySecret, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("Error creating config: %v", err)
	}

	client := s3.NewFromConfig(cfg)

	return &R2{
		Bucket: bucket,
		client: client,
	}, nil
}

// Upload uploads given body to given key
func (r2 *R2) Upload(ctx context.Context, key, contentType string, body []byte) error {
	_, err := r2.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r2.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("Error uploading object: %v", err)
	}

	return nil
}
