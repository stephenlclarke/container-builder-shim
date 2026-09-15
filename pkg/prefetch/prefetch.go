//===----------------------------------------------------------------------===//
// Copyright © 2025-2026 Apple Inc. and the container-builder-shim project authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//===----------------------------------------------------------------------===//

package prefetcher

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

var (
	ErrOffsetOutOfRange = errors.New("offset is out of range")
	ErrReadFailed       = errors.New("read from underlying reader failed")
	ErrPrefetcherClosed = errors.New("prefetcher is closed")
)

type Config struct {
	// WindowSize represents the maximum size of the sliding window for prefetching
	WindowSize int64

	// ChunkSize defines the size of chunks to read from the underlying reader
	ChunkSize int

	// MaxParallelReads limits the number of concurrent reads from the underlying reader
	MaxParallelReads int

	ReadTimeout   time.Duration
	RetryInterval time.Duration
	MaxRetries    int
}

func DefaultConfig() Config {
	return Config{
		WindowSize:       1 << 20,
		ChunkSize:        1 << 16,
		MaxParallelReads: 4,
		ReadTimeout:      1<<63 - 1,
		RetryInterval:    500 * time.Millisecond,
		MaxRetries:       3,
	}
}

type ReaderAt interface {
	io.ReaderAt
}

type Prefetcher interface {
	ReadAt(p []byte, off int64) (n int, err error)
	Size() int64
	Close() error
}

type prefetcher struct {
	reader     ReaderAt
	size       int64
	config     Config
	cache      *chunkCache
	semaphore  chan struct{}
	readCount  atomic.Int64
	ctx        context.Context
	cancelFunc context.CancelFunc
	lifecycle  sync.Mutex
	wg         sync.WaitGroup
	closed     atomic.Bool
	group      singleflight.Group
}

func New(reader ReaderAt, size int64, configs ...Config) (Prefetcher, error) {
	if reader == nil {
		return nil, errors.New("reader cannot be nil")
	}

	config := DefaultConfig()
	if len(configs) > 0 {
		config = configs[0]
	}

	if config.WindowSize <= 0 {
		config.WindowSize = DefaultConfig().WindowSize
	}
	if config.ChunkSize <= 0 {
		config.ChunkSize = DefaultConfig().ChunkSize
	}
	if config.MaxParallelReads <= 0 {
		config.MaxParallelReads = DefaultConfig().MaxParallelReads
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = DefaultConfig().ReadTimeout
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = DefaultConfig().RetryInterval
	}
	if config.MaxRetries < 0 {
		config.MaxRetries = DefaultConfig().MaxRetries
	}
	if config.WindowSize < int64(config.ChunkSize) {
		config.WindowSize = int64(config.ChunkSize)
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &prefetcher{
		reader:     reader,
		size:       size,
		config:     config,
		cache:      newChunkCache(config.ChunkSize),
		semaphore:  make(chan struct{}, config.MaxParallelReads),
		ctx:        ctx,
		cancelFunc: cancel,
	}

	return p, nil
}

func (p *prefetcher) ReadAt(b []byte, off int64) (int, error) {
	if !p.beginOperation() {
		return 0, ErrPrefetcherClosed
	}
	defer p.wg.Done()

	if err := p.validateRead(b, off); err != nil || len(b) == 0 {
		return 0, err
	}

	p.readCount.Add(1)
	defer p.readCount.Add(-1)

	startChunkIdx := off / int64(p.config.ChunkSize)
	endChunkIdx := (off + int64(len(b)) - 1) / int64(p.config.ChunkSize)
	p.cache.evictBefore(startChunkIdx)

	if p.size > 0 && off+int64(len(b)) > p.size && startChunkIdx == endChunkIdx {
		return 0, io.EOF
	}

	p.scheduleRequiredChunks(startChunkIdx, endChunkIdx)
	p.scheduleReadAhead(endChunkIdx, p.windowEndChunk(startChunkIdx))
	bytesRead, err := p.copyChunks(b, off, startChunkIdx, endChunkIdx)
	if err == nil && p.size > 0 && off+int64(bytesRead) >= p.size {
		err = io.EOF
	}
	return bytesRead, err
}

func (p *prefetcher) validateRead(buffer []byte, offset int64) error {
	if offset < 0 {
		return ErrOffsetOutOfRange
	}
	if len(buffer) == 0 {
		return nil
	}
	if p.size == 0 && len(buffer) > 0 {
		return io.EOF
	}
	if p.size > 0 && offset >= p.size {
		return ErrOffsetOutOfRange
	}
	return nil
}

func (p *prefetcher) windowEndChunk(startChunk int64) int64 {
	chunkSize := int64(p.config.ChunkSize)
	windowChunks := (p.config.WindowSize + chunkSize - 1) / chunkSize
	if windowChunks < 1 {
		windowChunks = 1
	}
	windowEnd := startChunk + windowChunks - 1
	if p.size > 0 {
		maxChunk := (p.size - 1) / chunkSize
		if windowEnd > maxChunk {
			return maxChunk
		}
	}
	return windowEnd
}

func (p *prefetcher) scheduleRequiredChunks(startChunk, endChunk int64) {
	for index := startChunk; index <= endChunk; index++ {
		if p.cache.hasChunk(index) || !p.beginOperation() {
			continue
		}
		go func(chunkIndex int64) {
			defer p.wg.Done()
			if p.ctx.Err() == nil && !p.cache.hasChunk(chunkIndex) {
				_, _ = p.fetchChunk(chunkIndex)
			}
		}(index)
	}
}

func (p *prefetcher) scheduleReadAhead(endChunk, windowEnd int64) {
	limit := p.config.WindowSize / int64(p.config.ChunkSize)
	if limit > 1000 {
		limit = 1000
	}
	scheduled := int64(0)
	for index := endChunk + 1; index <= windowEnd && scheduled < limit; index++ {
		if p.cache.hasChunk(index) || !p.beginOperation() {
			continue
		}
		scheduled++
		go p.readAhead(index)
	}
}

func (p *prefetcher) readAhead(index int64) {
	defer p.wg.Done()
	time.Sleep(5 * time.Millisecond)
	if p.ctx.Err() != nil || p.cache.hasChunk(index) {
		return
	}
	select {
	case p.semaphore <- struct{}{}:
		defer func() { <-p.semaphore }()
		buffer := make([]byte, p.config.ChunkSize)
		n, err := p.reader.ReadAt(buffer, index*int64(p.config.ChunkSize))
		if err == nil || err == io.EOF {
			p.cache.addChunk(&chunk{index: index, data: buffer, size: n, lastUsed: time.Now()})
		}
	default:
	}
}

func (p *prefetcher) copyChunks(buffer []byte, offset, startChunk, endChunk int64) (int, error) {
	bytesRead := 0
	for index := startChunk; index <= endChunk && bytesRead < len(buffer); index++ {
		cachedChunk, err := p.getChunk(index)
		if err != nil {
			return bytesRead, err
		}
		chunkOffset := 0
		if index == startChunk {
			chunkOffset = int(offset % int64(p.config.ChunkSize))
		}
		available := cachedChunk.size - chunkOffset
		if available <= 0 {
			break
		}
		count := minimumInt(available, len(buffer)-bytesRead)
		copy(buffer[bytesRead:bytesRead+count], cachedChunk.data[chunkOffset:chunkOffset+count])
		bytesRead += count
	}
	return bytesRead, nil
}

func minimumInt(first, second int) int {
	if first < second {
		return first
	}
	return second
}

func (p *prefetcher) getChunk(chunkIdx int64) (*chunk, error) {
	if chunk := p.cache.getChunk(chunkIdx); chunk != nil {
		return chunk, nil
	}

	return p.fetchChunk(chunkIdx)
}

func (p *prefetcher) fetchChunk(chunkIdx int64) (*chunk, error) {
	key := strconv.FormatInt(chunkIdx, 10)
	val, err, _ := p.group.Do(key, func() (interface{}, error) {
		if ch := p.cache.getChunk(chunkIdx); ch != nil {
			return ch, nil
		}
		select {
		case <-p.ctx.Done():
			return nil, p.ctx.Err()
		case p.semaphore <- struct{}{}:
			defer func() { <-p.semaphore }()
		case <-time.After(p.config.ReadTimeout):
			return nil, ErrReadFailed
		}
		buffer, n, readErr := p.readChunkWithRetries(chunkIdx)
		if readErr != nil && readErr != io.EOF {
			return nil, ErrReadFailed
		}
		ch := &chunk{index: chunkIdx, data: buffer, size: n, lastUsed: time.Now()}
		p.cache.addChunk(ch)
		return ch, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*chunk), nil
}

type chunkReadResult struct {
	n   int
	err error
}

func (p *prefetcher) readChunkWithRetries(chunkIndex int64) ([]byte, int, error) {
	var buffer []byte
	var n int
	var err error
	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 && !p.waitForRetry() {
			return nil, 0, p.ctx.Err()
		}
		buffer, n, err = p.readChunkOnce(chunkIndex)
		if err == nil || err == io.EOF {
			return buffer, n, err
		}
	}
	return buffer, n, err
}

func (p *prefetcher) waitForRetry() bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(p.config.RetryInterval):
		return true
	}
}

func (p *prefetcher) readChunkOnce(chunkIndex int64) ([]byte, int, error) {
	buffer := make([]byte, p.config.ChunkSize)
	resultChannel := make(chan chunkReadResult, 1)
	go func() {
		n, err := p.reader.ReadAt(buffer, chunkIndex*int64(p.config.ChunkSize))
		resultChannel <- chunkReadResult{n: n, err: err}
	}()

	readContext, cancel := context.WithTimeout(p.ctx, p.config.ReadTimeout)
	defer cancel()
	select {
	case <-readContext.Done():
		return buffer, 0, ErrReadFailed
	case result := <-resultChannel:
		return buffer, result.n, result.err
	}
}

func (p *prefetcher) Size() int64 {
	return p.size
}

// beginOperation registers work only while shutdown has not started.
func (p *prefetcher) beginOperation() bool {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()

	if p.closed.Load() {
		return false
	}

	p.wg.Add(1)
	return true
}

func (p *prefetcher) Close() error {
	p.lifecycle.Lock()
	if p.closed.Load() {
		p.lifecycle.Unlock()
		return nil
	}
	p.closed.Store(true)
	p.cancelFunc()
	p.lifecycle.Unlock()

	p.wg.Wait()
	p.cache.clear()

	return nil
}
