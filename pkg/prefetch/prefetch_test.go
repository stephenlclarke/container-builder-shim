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
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"
)

type mockReaderAt struct {
	data       []byte
	mutex      sync.Mutex
	readDelay  time.Duration
	errorAfter int
	readCount  int
}

func newMockReaderAt(size int, readDelay time.Duration, errorAfter int) *mockReaderAt {
	data := make([]byte, size)

	for i := 0; i < size; i++ {
		data[i] = byte(i % 256)
	}

	return &mockReaderAt{
		data:       data,
		readDelay:  readDelay,
		errorAfter: errorAfter,
	}
}

func (m *mockReaderAt) ReadAt(p []byte, off int64) (int, error) {
	m.mutex.Lock()
	m.readCount++
	readCount := m.readCount
	m.mutex.Unlock()

	if m.readDelay > 0 {
		time.Sleep(m.readDelay)
	}

	if m.errorAfter > 0 && readCount > m.errorAfter {
		return 0, errors.New("simulated read error")
	}

	if off < 0 {
		return 0, errors.New("negative offset")
	}

	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}

	n := len(p)
	remaining := int64(len(m.data)) - off
	if int64(n) > remaining {
		n = int(remaining)
	}

	copy(p, m.data[off:off+int64(n)])

	var err error
	if off+int64(n) >= int64(len(m.data)) {
		err = io.EOF
	}

	return n, err
}

func (m *mockReaderAt) GetReadCount() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.readCount
}

func TestPrefetcherBasic(t *testing.T) {
	mockReader := newMockReaderAt(100*1024, 0, 0)

	prefetcher, err := New(mockReader, int64(len(mockReader.data)))
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()

	buf := make([]byte, 1024)
	n, err := prefetcher.ReadAt(buf, 1024)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if n != 1024 {
		t.Errorf("Expected to read 1024 bytes, got %d", n)
	}

	for i := 0; i < n; i++ {
		expected := byte((i + 1024) % 256)
		if buf[i] != expected {
			t.Errorf("Data mismatch at offset %d: expected %d, got %d", i+1024, expected, buf[i])
			break
		}
	}
}

type sliceReaderAt struct{ data []byte }

func (s *sliceReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestReadAt(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChunkSize = 10
	cfg.WindowSize = 20

	data := []byte("ABCDEFGHIJKLMNO")
	reader := &sliceReaderAt{data}

	p, err := New(reader, int64(len(data)), cfg)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer p.Close()

	tests := []struct {
		name     string
		bufLen   int
		off      int64
		wantN    int
		wantErr  error
		wantData []byte
	}{
		{
			name:    "negative offset",
			bufLen:  1,
			off:     -1,
			wantN:   0,
			wantErr: ErrOffsetOutOfRange,
		},
		{
			name:    "zero-length read",
			bufLen:  0,
			off:     5,
			wantN:   0,
			wantErr: nil,
		},
		{
			name:    "offset out of range",
			bufLen:  1,
			off:     int64(len(data)),
			wantN:   0,
			wantErr: ErrOffsetOutOfRange,
		},
		{
			name:     "cross-chunk read",
			bufLen:   10,
			off:      5,
			wantN:    10,
			wantErr:  io.EOF,
			wantData: []byte("FGHIJKLMNO"),
		},
		{
			name:    "partial beyond EOF",
			bufLen:  10,
			off:     10,
			wantN:   0,
			wantErr: io.EOF,
		},
		{
			name:     "full read",
			bufLen:   len(data),
			off:      0,
			wantN:    len(data),
			wantErr:  io.EOF,
			wantData: data,
		},
	}

	for _, tc := range tests {
		buf := make([]byte, tc.bufLen)
		n, err := p.ReadAt(buf, tc.off)

		if n != tc.wantN {
			t.Errorf("[%s] got n=%d; want %d", tc.name, n, tc.wantN)
		}
		if err != tc.wantErr {
			t.Errorf("[%s] got err=%v; want %v", tc.name, err, tc.wantErr)
		}
		if tc.wantData != nil {
			if string(buf[:n]) != string(tc.wantData) {
				t.Errorf("[%s] data = %q; want %q", tc.name, buf[:n], tc.wantData)
			}
		}
	}
}

func TestPrefetcherConcurrentReads(t *testing.T) {
	mockReader := newMockReaderAt(1024*1024, 1*time.Millisecond, 0)

	config := DefaultConfig()
	config.ChunkSize = 4096
	config.WindowSize = 10 * 4096
	config.MaxParallelReads = 4

	prefetcher, err := New(mockReader, int64(len(mockReader.data)), config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()

	var wg sync.WaitGroup
	const numReaders = 10
	const readSize = 8192

	errors := make([]error, numReaders)
	data := make([][]byte, numReaders)
	offsets := concurrentReadOffsets(len(mockReader.data), readSize, numReaders)

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go performConcurrentRead(&wg, prefetcher, i, offsets[i], readSize, errors, data)
	}

	wg.Wait()

	for i, err := range errors {
		if err != nil {
			t.Errorf("Reader %d encountered error: %v", i, err)
		}
	}

	assertConcurrentReadData(t, data, offsets)

	if readCount := mockReader.GetReadCount(); readCount >= numReaders {
		t.Logf("Expected read count to be less than %d due to caching, but got %d",
			numReaders, readCount)
	}
}

func concurrentReadOffsets(dataSize, readSize, readers int) []int64 {
	offsets := make([]int64, readers)
	maximum := int64(dataSize - readSize)
	for index := range offsets {
		pairStart := index - index%2
		offsets[index] = int64(pairStart) * maximum / int64(readers)
	}
	return offsets
}

func performConcurrentRead(wg *sync.WaitGroup, prefetcher io.ReaderAt, index int, offset int64, readSize int, errors []error, data [][]byte) {
	defer wg.Done()
	buffer := make([]byte, readSize)
	n, err := prefetcher.ReadAt(buffer, offset)
	if err != nil && err != io.EOF {
		errors[index] = err
		return
	}
	if n != readSize {
		errors[index] = fmt.Errorf("incomplete read")
		return
	}
	data[index] = append([]byte(nil), buffer...)
}

func assertConcurrentReadData(t *testing.T, data [][]byte, offsets []int64) {
	t.Helper()
	for readerIndex, buffer := range data {
		for byteIndex, value := range buffer {
			expected := byte((int(offsets[readerIndex]) + byteIndex) % 256)
			if value != expected {
				t.Errorf("data mismatch in reader %d at offset %d: expected %d, got %d",
					readerIndex, offsets[readerIndex]+int64(byteIndex), expected, value)
				break
			}
		}
	}
}

type prefetcherEdgeCase struct {
	name       string
	dataSize   int
	chunkSize  int
	windowSize int64
	readOffset int64
	readSize   int
	expectErr  bool
}

func TestPrefetcherEdgeCases(t *testing.T) {
	tests := []prefetcherEdgeCase{
		{
			name:       "Read beyond EOF",
			dataSize:   1024,
			chunkSize:  256,
			windowSize: 512,
			readOffset: 1000,
			readSize:   100,
			expectErr:  false,
		},
		{
			name:       "Read completely beyond EOF",
			dataSize:   1024,
			chunkSize:  256,
			windowSize: 512,
			readOffset: 1024,
			readSize:   100,
			expectErr:  true,
		},
		{
			name:       "Zero-sized read",
			dataSize:   1024,
			chunkSize:  256,
			windowSize: 512,
			readOffset: 500,
			readSize:   0,
			expectErr:  false,
		},
		{
			name:       "Read across multiple chunks",
			dataSize:   1024,
			chunkSize:  100,
			windowSize: 300,
			readOffset: 95,
			readSize:   210,
			expectErr:  false,
		},
		{
			name:       "Chunk size equals window size",
			dataSize:   1024,
			chunkSize:  128,
			windowSize: 128,
			readOffset: 200,
			readSize:   50,
			expectErr:  false,
		},
		{
			name:       "Chunk size larger than window size",
			dataSize:   1024,
			chunkSize:  256,
			windowSize: 128,
			readOffset: 200,
			readSize:   50,
			expectErr:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runPrefetcherEdgeCase(t, tc)
		})
	}
}

func runPrefetcherEdgeCase(t *testing.T, testCase prefetcherEdgeCase) {
	t.Helper()
	reader := newMockReaderAt(testCase.dataSize, 0, 0)
	config := DefaultConfig()
	config.ChunkSize = testCase.chunkSize
	config.WindowSize = testCase.windowSize
	prefetcher, err := New(reader, int64(testCase.dataSize), config)
	if err != nil {
		t.Fatalf("failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()
	n, err := prefetcher.ReadAt(make([]byte, testCase.readSize), testCase.readOffset)
	if testCase.expectErr {
		if err == nil {
			t.Error("expected error but got none")
		}
		return
	}
	expectedSize := minimumInt(testCase.readSize, max(0, testCase.dataSize-int(testCase.readOffset)))
	if n != expectedSize {
		t.Errorf("expected to read %d bytes, got %d", expectedSize, n)
	}
	if testCase.readOffset+int64(testCase.readSize) >= int64(testCase.dataSize) && err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestPrefetcherReadFailure(t *testing.T) {
	mockReader := newMockReaderAt(100*1024, 0, 1)

	config := DefaultConfig()
	config.ChunkSize = 4 * 1024
	config.WindowSize = 8 * 1024
	config.MaxRetries = 0 // Don't retry

	prefetcher, err := New(mockReader, int64(len(mockReader.data)), config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()

	firstBuf := make([]byte, 1024)
	_, firstErr := prefetcher.ReadAt(firstBuf, 0)
	if firstErr != nil {
		t.Logf("First read error (acceptable): %v", firstErr)
	}

	time.Sleep(50 * time.Millisecond)

	var hadError bool
	for i := 2; i < 10 && !hadError; i++ {
		offset := int64(i * 8 * 1024)
		buf := make([]byte, 1024)
		_, err := prefetcher.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			t.Logf("Expected read failure at offset %d: %v", offset, err)
			hadError = true
		}
	}

	if !hadError {
		largeBuf := make([]byte, 50*1024)
		_, err := prefetcher.ReadAt(largeBuf, 20*1024)
		hadError = (err != nil && err != io.EOF)
	}

	if !hadError {
		t.Errorf("Failed to detect any errors from the underlying reader")
	} else {
		t.Logf("Successfully detected errors from the underlying reader as expected")
	}
}

func TestPrefetcherCancelAndClose(t *testing.T) {
	mockReader := newMockReaderAt(1024*1024, 10*time.Millisecond, 0)

	prefetcher, err := New(mockReader, int64(len(mockReader.data)))
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			buf := make([]byte, 8192)
			if _, err := prefetcher.ReadAt(buf, offset); err != nil && err != io.EOF && err != ErrPrefetcherClosed {
				t.Errorf("ReadAt(%d) returned %v", offset, err)
			}
		}(int64(i * 10000))
	}

	time.Sleep(20 * time.Millisecond) // Give time for reads to start
	err = prefetcher.Close()
	if err != nil {
		t.Errorf("Failed to close prefetcher: %v", err)
	}

	buf := make([]byte, 1024)
	_, err = prefetcher.ReadAt(buf, 0)
	if err != ErrPrefetcherClosed {
		t.Errorf("Expected ErrPrefetcherClosed, got %v", err)
	}

	wg.Wait()
}

func BenchmarkPrefetcher(b *testing.B) {
	dataSize := 10 * 1024 * 1024
	mockReader := newMockReaderAt(dataSize, 1*time.Millisecond, 0)

	prefetcher, err := New(mockReader, int64(dataSize))
	if err != nil {
		b.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()

	readSize := 4096
	buf := make([]byte, readSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		offset := rand.Int63n(int64(dataSize - readSize))
		_, err := prefetcher.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			b.Fatalf("Read failed: %v", err)
		}
	}
}

func BenchmarkDirectRead(b *testing.B) {
	dataSize := 10 * 1024 * 1024
	mockReader := newMockReaderAt(dataSize, 1*time.Millisecond, 0)

	readSize := 4096
	buf := make([]byte, readSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		offset := rand.Int63n(int64(dataSize - readSize))
		_, err := mockReader.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			b.Fatalf("Read failed: %v", err)
		}
	}
}
