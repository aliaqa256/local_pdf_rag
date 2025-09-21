package adapters

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"

	"rag-service/internal/infrastructure/config"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinIOAdapter struct {
	Client *minio.Client
	Config *config.Config
}

func NewMinIOAdapter(cfg *config.Config) (*MinIOAdapter, error) {
	client, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: cfg.MinIOUseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create MinIO client: %w", err)
	}

	// Test connection
	ctx := context.Background()
	_, err = client.ListBuckets(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MinIO: %w", err)
	}

	// Create default bucket if it doesn't exist
	bucketName := "documents"
	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to check bucket existence: %w", err)
	}

	if !exists {
		err = client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
		}
		log.Printf("✅ Created MinIO bucket: %s", bucketName)
	}

	log.Println("✅ MinIO connected successfully")

	return &MinIOAdapter{
		Client: client,
		Config: cfg,
	}, nil
}

func (m *MinIOAdapter) HealthCheck(ctx context.Context) error {
	_, err := m.Client.ListBuckets(ctx)
	return err
}

func (m *MinIOAdapter) UploadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	_, err := m.Client.FPutObject(ctx, bucketName, objectName, filePath, minio.PutObjectOptions{})
	return err
}

func (m *MinIOAdapter) DownloadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	return m.Client.FGetObject(ctx, bucketName, objectName, filePath, minio.GetObjectOptions{})
}

func (m *MinIOAdapter) GetObject(ctx context.Context, bucketName, objectName string) ([]byte, error) {
	object, err := m.Client.GetObject(ctx, bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %w", err)
	}
	defer object.Close()

	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("failed to read object data: %w", err)
	}

	return data, nil
}

func (m *MinIOAdapter) PutObject(ctx context.Context, bucketName, objectName string, data []byte, contentType string) error {
	reader := bytes.NewReader(data)
	_, err := m.Client.PutObject(ctx, bucketName, objectName, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

// FlushAllFiles removes all files from MinIO
func (m *MinIOAdapter) FlushAllFiles(ctx context.Context) error {
	// List all objects in the documents bucket
	objectCh := m.Client.ListObjects(ctx, "documents", minio.ListObjectsOptions{
		Recursive: true,
	})

	// Remove all objects
	for object := range objectCh {
		if object.Err != nil {
			return fmt.Errorf("error listing objects: %w", object.Err)
		}

		err := m.Client.RemoveObject(ctx, "documents", object.Key, minio.RemoveObjectOptions{})
		if err != nil {
			return fmt.Errorf("error removing object %s: %w", object.Key, err)
		}
	}

	log.Println("✅ All files flushed from MinIO successfully")
	return nil
}
