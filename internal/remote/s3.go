package remote

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectStore pushes a job's local checkpoint directory to S3 as a single
// gzipped tarball, via multipart upload so large bioinformatics checkpoints
// don't need to fit in memory or be buffered twice on local disk.
type ObjectStore struct {
	uploader *manager.Uploader
	bucket   string
	prefix   string
}

// NewObjectStore returns an ObjectStore writing under s3://bucket/prefix/.
func NewObjectStore(cfg aws.Config, bucket, prefix string) *ObjectStore {
	client := s3.NewFromConfig(cfg)
	return &ObjectStore{uploader: manager.NewUploader(client), bucket: bucket, prefix: prefix}
}

// Key returns the S3 key a job's checkpoint tarball is stored under.
func (o *ObjectStore) Key(jobID string) string {
	return path.Join(o.prefix, jobID+".tar.gz")
}

// URI returns the s3:// URI for a job's checkpoint object.
func (o *ObjectStore) URI(jobID string) string {
	return fmt.Sprintf("s3://%s/%s", o.bucket, o.Key(jobID))
}

// PushDir tars, gzips, and uploads checkpointDir to S3, streaming through a
// pipe so the compressed tarball is never fully materialized on disk. It
// returns the number of (compressed) bytes uploaded.
func (o *ObjectStore) PushDir(ctx context.Context, jobID, checkpointDir string) (int64, error) {
	pr, pw := io.Pipe()
	cw := &countingWriter{w: pw}

	go func() {
		pw.CloseWithError(tarGzDir(checkpointDir, cw))
	}()

	_, err := o.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(o.Key(jobID)),
		Body:   pr,
	})
	if err != nil {
		return cw.n, fmt.Errorf("upload checkpoint for job %s: %w", jobID, err)
	}
	return cw.n, nil
}

func tarGzDir(dir string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return fmt.Errorf("tar %s: %w", dir, err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	return nil
}

// PullFromURI downloads and extracts the gzipped tarball at an s3:// URI
// (as recorded by StatusStore.Complete) into destDir. It takes the URI
// rather than a bucket/prefix pair since a restore only ever needs the one
// object already named in the job's record.
func PullFromURI(ctx context.Context, cfg aws.Config, s3URI, destDir string) error {
	bucket, key, err := parseS3URI(s3URI)
	if err != nil {
		return err
	}

	client := s3.NewFromConfig(cfg)
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("download %s: %w", s3URI, err)
	}
	defer out.Body.Close()

	if err := untarGz(out.Body, destDir); err != nil {
		return fmt.Errorf("extract %s: %w", s3URI, err)
	}
	return nil
}

func parseS3URI(uri string) (bucket, key string, err error) {
	const prefix = "s3://"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", fmt.Errorf("not an s3:// uri: %s", uri)
	}
	rest := uri[len(prefix):]
	idx := strings.IndexByte(rest, '/')
	if idx < 0 {
		return "", "", fmt.Errorf("s3 uri missing object key: %s", uri)
	}
	return rest[:idx], rest[idx+1:], nil
}

func untarGz(r io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", destDir, err)
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	cleanDest := filepath.Clean(destDir)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destDir, hdr.Name)
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
