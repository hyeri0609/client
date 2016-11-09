package chat

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/keybase/client/go/chat/s3"
	"github.com/keybase/client/go/libkb"
	"github.com/keybase/client/go/logger"
	"github.com/keybase/client/go/protocol/chat1"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

type ProgressReporter func(bytesCompleted, bytesTotal int)

const minMultiSize = 5 * 1024 * 1024 // can't use Multi API with parts less than 5MB
const blockSize = 5 * 1024 * 1024    // 5MB is the minimum Multi part size

// PutS3Result is the success result of calling PutS3.
type PutS3Result struct {
	Region   string
	Endpoint string
	Bucket   string
	Path     string
	Size     int64
}

type UploadTask struct {
	S3Params  chat1.S3Params
	LocalSrc  chat1.LocalSource
	Plaintext io.Reader
	S3Signer  s3.Signer
	Progress  ProgressReporter
}

func UploadAsset(ctx context.Context, log logger.Logger, task *UploadTask) (chat1.Asset, error) {
	// encrypt the stream
	enc := NewSignEncrypter()
	len := enc.EncryptedLen(task.LocalSrc.Size)
	encReader, err := enc.Encrypt(task.Plaintext)
	if err != nil {
		return chat1.Asset{}, err
	}

	// compute ciphertext hash
	hash := sha256.New()
	tee := io.TeeReader(encReader, hash)

	// post to s3
	upRes, err := PutS3(ctx, log, tee, int64(len), task.S3Params, task.S3Signer, task.Progress, task.LocalSrc)
	if err != nil {
		return chat1.Asset{}, err
	}
	log.Debug("chat attachment upload: %+v", upRes)

	asset := chat1.Asset{
		Filename:  filepath.Base(task.LocalSrc.Filename),
		Region:    upRes.Region,
		Endpoint:  upRes.Endpoint,
		Bucket:    upRes.Bucket,
		Path:      upRes.Path,
		Size:      int(upRes.Size),
		Key:       enc.EncryptKey(),
		VerifyKey: enc.VerifyKey(),
		EncHash:   hash.Sum(nil),
	}
	return asset, nil
}

// PutS3 uploads the data in Reader r to S3.  It chooses whether to use
// putSingle or putMultiPipeline based on the size of the object.
func PutS3(ctx context.Context, log logger.Logger, r io.Reader, size int64, params chat1.S3Params, signer s3.Signer, progress ProgressReporter, local chat1.LocalSource) (*PutS3Result, error) {
	region := s3.Region{
		Name:             params.RegionName,
		S3Endpoint:       params.RegionEndpoint,
		S3BucketEndpoint: params.RegionBucketEndpoint,
	}
	conn := s3.New(signer, region)
	conn.AccessKey = params.AccessKey

	b := conn.Bucket(params.Bucket)

	if size <= minMultiSize {
		if err := putSingle(ctx, log, r, size, params, b, progress); err != nil {
			return nil, err
		}
	} else {
		objectKey, err := putMultiPipeline(ctx, log, r, size, params, b, progress, local)
		if err != nil {
			return nil, err
		}
		params.ObjectKey = objectKey
	}

	res := PutS3Result{
		Region:   params.RegionName,
		Endpoint: params.RegionEndpoint,
		Bucket:   params.Bucket,
		Path:     params.ObjectKey,
		Size:     size,
	}

	return &res, nil
}

// DownloadAsset gets an object from S3 as described in asset.
func DownloadAsset(ctx context.Context, log logger.Logger, params chat1.S3Params, asset chat1.Asset, w io.Writer, signer s3.Signer, progress ProgressReporter) error {
	if asset.Key == nil || asset.VerifyKey == nil || asset.EncHash == nil {
		return fmt.Errorf("unencrypted attachments not supported")
	}
	region := s3.Region{
		Name:       asset.Region,
		S3Endpoint: asset.Endpoint,
	}
	conn := s3.New(signer, region)
	conn.AccessKey = params.AccessKey

	b := conn.Bucket(asset.Bucket)

	log.Debug("downloading %s from s3", asset.Path)
	body, err := b.GetReader(asset.Path)
	defer func() {
		if body != nil {
			body.Close()
		}
	}()
	if err != nil {
		return err
	}

	// compute hash
	hash := sha256.New()
	verify := io.TeeReader(body, hash)

	// to keep track of download progress
	progWriter := newProgressWriter(progress, asset.Size)
	tee := io.TeeReader(verify, progWriter)

	// decrypt body
	dec := NewSignDecrypter()
	decBody := dec.Decrypt(tee, asset.Key, asset.VerifyKey)
	if err != nil {
		return err
	}

	n, err := io.Copy(w, decBody)
	if err != nil {
		return err
	}

	log.Debug("downloaded and decrypted to %d plaintext bytes", n)

	// validate the EncHash
	if !hmac.Equal(asset.EncHash, hash.Sum(nil)) {
		return fmt.Errorf("invalid attachment content hash")
	}
	log.Debug("attachment content hash is valid")

	return nil
}

// putSingle uploads data in r to S3 with the Put API.  It has to be
// used for anything less than 5MB.  It can be used for anything up
// to 5GB, but putMultiPipeline best for anything over 5MB.
func putSingle(ctx context.Context, log logger.Logger, r io.Reader, size int64, params chat1.S3Params, b *s3.Bucket, progress ProgressReporter) error {
	log.Debug("s3 putSingle (size = %d)", size)

	// In order to be able to retry the upload, need to read in the entire
	// attachment.  But putSingle is only called for attachments <= 5MB, so
	// this isn't horrible.
	buf := make([]byte, size)
	n, err := io.ReadFull(r, buf)
	if err != nil {
		return err
	}
	if int64(n) != size {
		return fmt.Errorf("invalid read attachment size: %d (expected %d)", n, size)
	}
	sr := bytes.NewReader(buf)

	progWriter := newProgressWriter(progress, int(size))
	tee := io.TeeReader(sr, progWriter)

	var lastErr error
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(libkb.BackoffDefault.Duration(i)):
		}
		log.Debug("s3 putSingle attempt %d", i+1)
		err := b.PutReader(params.ObjectKey, tee, size, "application/octet-stream", s3.ACL(params.Acl), s3.Options{})
		if err == nil {
			log.Debug("putSingle attempt %d success", i+1)
			return nil
		}
		log.Debug("putSingle attempt %d error: %s", i+1, err)
		lastErr = err

		// move back to beginning of sr buffer for retry
		sr.Seek(0, io.SeekStart)
		progWriter = newProgressWriter(progress, int(size))
		tee = io.TeeReader(sr, progWriter)
	}
	return fmt.Errorf("failed putSingle (last error: %s)", lastErr)
}

// putMultiPipeline uploads data in r to S3 using the Multi API.  It uses a
// pipeline to upload 10 blocks of data concurrently.  Each block is 5MB.
// It returns the object key if no errors.  putMultiPipeline will return
// a different object key from params.ObjectKey if a previous Put is
// successfully resumed and completed.
func putMultiPipeline(ctx context.Context, log logger.Logger, r io.Reader, size int64, params chat1.S3Params, b *s3.Bucket, progress ProgressReporter, local chat1.LocalSource) (string, error) {
	log.Debug("s3 putMultiPipeline (size = %d)", size)

	// putMulti operations are resumable
	stash := NewFileStash()
	previous := false
	previousObjectKey, err := stash.Lookup(local.Filename)
	if err == nil && previousObjectKey != "" {
		previous = true
	}
	if previous {
		log.Debug("found previous object key for %s: %s", local.Filename, previousObjectKey)
		params.ObjectKey = previousObjectKey
	} else {
		log.Debug("storing object key for %s: %s", local.Filename, params.ObjectKey)
		if err := stash.Start(local.Filename, params.ObjectKey); err != nil {
			log.Debug("ignoring attachment stash Start error: %s", err)
		}
	}

	multi, err := b.InitMulti(params.ObjectKey, "application/octet-stream", s3.ACL(params.Acl))
	if err != nil {
		log.Debug("InitMulti error: %s", err)
		return "", err
	}

	var previousParts []s3.Part
	if previous {
		previousParts, err = multi.ListParts()
		if err != nil {
			log.Debug("ignoring multi.ListParts error: %s", err)
		}
	}

	type job struct {
		block []byte
		index int
	}
	eg, ctx := errgroup.WithContext(ctx)
	blockCh := make(chan job)
	retCh := make(chan s3.Part)
	eg.Go(func() error {
		defer close(blockCh)
		var partNumber int
		for {
			partNumber++
			block := make([]byte, blockSize)
			// must call io.ReadFull to ensure full block read
			n, err := io.ReadFull(r, block)
			// io.ErrUnexpectedEOF will be returned for last partial block,
			// which is ok.
			if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
				return err
			}
			if n < blockSize {
				block = block[:n]
			}
			if n > 0 {
				select {
				case blockCh <- job{block: block, index: partNumber}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
		}
		return nil
	})
	for i := 0; i < 10; i++ {
		eg.Go(func() error {
			for b := range blockCh {
				log.Debug("start: upload part %d", b.index)
				if previous && len(previousParts) > b.index {
					// check previousParts for this block
					p := previousParts[b.index]
					md5sum := md5.Sum(b.block)
					md5hex := `"` + hex.EncodeToString(md5sum[:]) + `"`
					if int(p.Size) == len(b.block) && p.ETag == md5hex {
						log.Debug("part %d already uploaded")
						select {
						case retCh <- p:
						case <-ctx.Done():
							return ctx.Err()
						}
						continue
					}
				}
				part, putErr := putRetry(ctx, log, multi, b.index, b.block)
				if putErr != nil {
					return putErr
				}
				select {
				case retCh <- part:
				case <-ctx.Done():
					return ctx.Err()
				}
				log.Debug("finish: upload part %d", b.index)
			}
			return nil
		})
	}

	go func() {
		eg.Wait()
		close(retCh)
	}()

	var complete int64
	var parts []s3.Part
	for p := range retCh {
		parts = append(parts, p)
		complete += p.Size
		if progress != nil {
			progress(int(complete), int(size))
		}
	}
	if err := eg.Wait(); err != nil {
		return "", err
	}

	log.Debug("s3 putMulti all parts uploaded, completing request")

	if err = multi.Complete(parts); err != nil {
		log.Debug("multi.Complete error: %s", err)
		return "", err
	}
	log.Debug("s3 putMulti success, %d parts", len(parts))

	if err := stash.Stop(local.Filename); err != nil {
		log.Debug("ignoring attachment stash.Stop error: %s", err)
	}

	return params.ObjectKey, nil
}

// putRetry sends a block to S3, retrying 10 times w/ backoff.
func putRetry(ctx context.Context, log logger.Logger, multi *s3.Multi, partNumber int, block []byte) (s3.Part, error) {
	var lastErr error
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return s3.Part{}, ctx.Err()
		case <-time.After(libkb.BackoffDefault.Duration(i)):
		}
		log.Debug("attempt %d to upload part %d", i+1, partNumber)
		part, putErr := multi.PutPart(partNumber, bytes.NewReader(block))
		if putErr == nil {
			log.Debug("success in attempt %d to upload part %d", i+1, partNumber)
			return part, nil
		}
		log.Debug("error in attempt %d to upload part %d: %s", i+1, putErr)
		lastErr = putErr
	}
	return s3.Part{}, fmt.Errorf("failed to put part %d (last error: %s)", partNumber, lastErr)
}

func checkResumable(local chat1.LocalSource) (string, bool) {
	stash := NewFileStash()
	existing, err := stash.Lookup(local.Filename)
	if err != nil {
		return "", false
	}
	if existing == "" {
		return "", false
	}

	return existing, true
}

type progressWriter struct {
	complete   int
	total      int
	lastReport int
	progress   ProgressReporter
}

func newProgressWriter(p ProgressReporter, size int) *progressWriter {
	return &progressWriter{progress: p, total: size}
}

func (p *progressWriter) Write(data []byte) (n int, err error) {
	n = len(data)
	p.complete += n
	percent := (100 * p.complete) / p.total
	if percent > p.lastReport {
		if p.progress != nil {
			p.progress(p.complete, p.total)
		}
		p.lastReport = percent
	}
	return n, nil
}
