package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

const Scheme = "s3"

// Config configures the S3/MinIO storage driver.
type Config struct {
	Bucket       string
	Region       string
	Endpoint     string // Optional custom endpoint for MinIO, Cloudflare R2, or LocalStack
	AccessKey    string
	SecretKey    string
	UsePathStyle bool   // Set true for MinIO and LocalStack
}

// Driver implements storage.Driver for AWS S3 and MinIO.
type Driver struct {
	cfg           Config
	client        *s3.Client
	presignClient *s3.PresignClient
}

// New creates a new S3 storage driver.
func New(cfg Config) (*Driver, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("storage/s3: bucket name is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	opts := s3.Options{
		Region:       cfg.Region,
		UsePathStyle: cfg.UsePathStyle,
	}

	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts.Credentials = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	}

	client := s3.New(opts)
	presignClient := s3.NewPresignClient(client)

	d := &Driver{
		cfg:           cfg,
		client:        client,
		presignClient: presignClient,
	}

	return d, nil
}

func (d *Driver) Scheme() string {
	return Scheme
}

func (d *Driver) cleanKey(storageRef string) string {
	key := storageRef
	key = strings.TrimPrefix(key, "s3://")
	key = strings.TrimPrefix(key, "minio://")
	// If path includes bucket prefix, strip it
	if strings.HasPrefix(key, d.cfg.Bucket+"/") {
		key = strings.TrimPrefix(key, d.cfg.Bucket+"/")
	}
	return strings.TrimPrefix(key, "/")
}

// GenerateUploadSlot generates a pre-signed PUT URL using S3 PresignClient.
func (d *Driver) GenerateUploadSlot(ctx context.Context, storageRef string, ttl time.Duration) (*protocol.UploadSlotResponse, error) {
	validTTL, err := storage.ValidateEphemeralTTL(ttl)
	if err != nil {
		return nil, err
	}

	key := d.cleanKey(storageRef)

	req, err := d.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(d.cfg.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(validTTL))
	if err != nil {
		return nil, fmt.Errorf("storage/s3: presign put object failed: %w", err)
	}

	return &protocol.UploadSlotResponse{
		TargetURL:        req.URL,
		HTTPMethod:       http.MethodPut,
		StorageReference: fmt.Sprintf("s3://%s/%s", d.cfg.Bucket, key),
		ExpiredAt:        time.Now().Add(validTTL),
	}, nil
}

// StreamRead streams object bytes directly from S3 without saving to local disk.
func (d *Driver) StreamRead(ctx context.Context, sourceURI string) (io.ReadCloser, error) {
	key := d.cleanKey(sourceURI)

	resp, err := d.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("storage/s3: get object failed: %w", err)
	}

	return resp.Body, nil
}

// PutObject uploads an object stream to S3.
func (d *Driver) PutObject(ctx context.Context, storageRef string, r io.Reader, size int64) error {
	key := d.cleanKey(storageRef)

	input := &s3.PutObjectInput{
		Bucket: aws.String(d.cfg.Bucket),
		Key:    aws.String(key),
		Body:   r,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}

	_, err := d.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("storage/s3: put object failed: %w", err)
	}
	return nil
}

// DeleteObject deletes an object from S3.
func (d *Driver) DeleteObject(ctx context.Context, storageRef string) error {
	key := d.cleanKey(storageRef)

	_, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(d.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("storage/s3: delete object failed: %w", err)
	}
	return nil
}

// RegisterMinIO creates and registers an S3 driver instance with Scheme "minio".
func RegisterMinIO(cfg Config) (*Driver, error) {
	cfg.UsePathStyle = true
	d, err := New(cfg)
	if err != nil {
		return nil, err
	}
	storage.Register(&minioDriver{Driver: d})
	return d, nil
}

type minioDriver struct {
	*Driver
}

func (m *minioDriver) Scheme() string {
	return "minio"
}
