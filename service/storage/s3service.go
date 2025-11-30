package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	defaultBucketName = "ai-contacts"
	defaultPrefix     = "base-faces/"
)

// Config controls how the S3 storage service behaves.
type Config struct {
	Bucket string
	Prefix string
	Region string
}

// Service uploads generated assets to the configured S3 bucket/prefix.
type Service struct {
	client   *s3.Client
	uploader *manager.Uploader
	bucket   string
	prefix   string
	region   string
}

// New constructs a Service that uploads to the ai-contacts/base-faces prefix by default.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		bucket = defaultBucketName
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix == "" {
		prefix = defaultPrefix
	}
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	requestedRegion := strings.TrimSpace(cfg.Region)

	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if requestedRegion != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(requestedRegion))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg)
	effectiveRegion := awsCfg.Region
	if detectedRegion, err := manager.GetBucketRegion(ctx, client, bucket); err == nil && strings.TrimSpace(detectedRegion) != "" {
		effectiveRegion = detectedRegion
		awsCfg.Region = detectedRegion
		client = s3.NewFromConfig(awsCfg)
	}
	return &Service{
		client:   client,
		uploader: manager.NewUploader(client),
		bucket:   bucket,
		prefix:   prefix,
		region:   effectiveRegion,
	}, nil
}

// UploadImage stores the provided image bytes in S3 using the configured prefix and returns the s3:// URI.
func (s *Service) UploadImage(ctx context.Context, objectName, contentType string, data []byte) (string, error) {
	if s == nil || s.uploader == nil {
		return "", fmt.Errorf("storage service not initialized")
	}
	objectName = strings.TrimSpace(objectName)
	if objectName == "" {
		return "", fmt.Errorf("object name is required")
	}
	contentType = strings.TrimSpace(contentType)
	if len(data) == 0 {
		return "", fmt.Errorf("image data is empty")
	}
	key := path.Join(s.prefix, objectName)
	if !strings.Contains(key, ".") {
		key += ".png"
	}
	body := bytes.NewReader(data)
	if err := s.upload(ctx, key, contentType, body); err != nil {
		return "", err
	}
	return s.httpURL(key), nil
}

func (s *Service) upload(ctx context.Context, key, contentType string, body io.Reader) error {
	_, err := s.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        body,
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("upload to s3: %w", err)
	}
	return nil
}

func (s *Service) httpURL(key string) string {
	region := strings.TrimSpace(s.region)
	if region == "" || region == "us-east-1" {
		return fmt.Sprintf("https://%s.s3.amazonaws.com/%s", s.bucket, key)
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", s.bucket, region, key)
}
