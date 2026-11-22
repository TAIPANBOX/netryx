package rollup

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// version is the format of the bucket files this writes. Version 1 (an
// organisation by number alone, no naming source) still loads; any other
// version is refused rather than guessed at.
const version = 2

// header opens every bucket file. Open marks a file SaveOpen wrote while
// its bucket was still filling: it is not final, so SaveClosed writes the
// bucket again once it closes.
type header struct {
	Version int
	Saved   time.Time
	Open    bool
}

// fileKey names one bucket file.
type fileKey struct {
	dir string
	at  int64
}

// takeLocked adds a bucket read from a file to RAM, once per file: an hour
// to the hours, and to its day unless that day was read from a final file
// (which holds it already); a day to the days. The caller holds the lock.
func (s *Store) takeLocked(k kind, bk *bucket) {
	fk := fileKey{k.dir, bk.At.Unix()}
	if s.taken[fk] {
		return
	}
	s.taken[fk] = true
	delete(s.future, fk)
	if dst := k.ring(s).at(bk.At); dst != nil {
		s.merge(dst, bk)
	}
	if k.step == day {
		s.finalDay[bk.At.Unix()] = true
		return
	}
	d := bk.At.Truncate(day)
	if !s.finalDay[d.Unix()] {
		if dst := s.dys.at(d); dst != nil {
			s.merge(dst, bk)
		}
	}
}

// kind is one directory of buckets: hours or days.
type kind struct {
	dir   string
	step  time.Duration
	name  func(time.Time) string
	parse func(string) (time.Time, bool)
	ring  func(*Store) *coarse
}

var kinds = []kind{
	{
		dir:  "hours",
		step: time.Hour,
		name: func(t time.Time) string { return strconv.FormatInt(t.Unix()/3600, 10) + ".gob" },
		parse: func(n string) (time.Time, bool) {
			h, err := strconv.ParseInt(strings.TrimSuffix(n, ".gob"), 10, 64)
			t := time.Unix(h*3600, 0).UTC()
			// Only the exact name this writes: "007.gob" is not hour 7.
			return t, err == nil && strconv.FormatInt(h, 10)+".gob" == n
		},
		ring: func(s *Store) *coarse { return &s.hrs },
	},
	{
		dir:  "days",
		step: day,
		name: func(t time.Time) string { return t.Format("20060102") + ".gob" },
		parse: func(n string) (time.Time, bool) {
			t, err := time.Parse("20060102", strings.TrimSuffix(n, ".gob"))
			return t, err == nil && t.Format("20060102")+".gob" == n
		},
		ring: func(s *Store) *coarse { return &s.dys },
	},
}

// SaveClosed writes every closed hour and day the store holds that has no
// final file yet, to dir/hours/<unix hour>.gob and dir/days/<yyyymmdd>.gob,
// and deletes files the rings no longer reach. A bucket is closed a minute
// after it ends, and takes no delta after that (Late counts them), so it is
// written once, after its last delta: flash takes one small file an hour
// instead of the whole history. A file SaveOpen left is not final and is
// written over once its hour closes, so an hour is written at most twice.
// Each file is written beside its name and renamed over it.
//
// Callers must serialise SaveClosed and SaveOpen on one directory: two at
// once may both find a bucket unwritten, and one may delete the other's
// temporary file.
func SaveClosed(s *Store, dir string) (int, error) {
	return save(s, dir, s.now().UTC(), false)
}

// SaveOpen is for a graceful stop: it writes what SaveClosed would, and also
// every hour not closed yet (the current one, and one still in its grace
// minute), marked open, so a restart loses nothing. LoadDir takes those
// hours back, where they keep filling. The open day is not written: on the
// next start its hours rebuild it, so a power cut later in the day cannot
// leave a stale copy of it behind. A final file is never written over.
// Callers must serialise it with SaveClosed.
func SaveOpen(s *Store, dir string) error {
	_, err := save(s, dir, s.now().UTC(), true)
	return err
}

func save(s *Store, dir string, now time.Time, open bool) (int, error) {
	written := 0
	var errs []error
	for _, k := range kinds {
		sub := filepath.Join(dir, k.dir)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		s.mu.RLock()
		c := k.ring(s)
		var shut, filling []time.Time
		for _, bk := range c.each(c.oldest(), c.newest) {
			if closed(bk.At, k.step, now) {
				shut = append(shut, bk.At)
			} else if open && k.step == time.Hour {
				filling = append(filling, bk.At)
			}
		}
		keep := now.Truncate(k.step).Add(-time.Duration(c.n-1) * k.step)
		s.mu.RUnlock()

		write := func(at time.Time, isOpen bool) {
			path := filepath.Join(sub, k.name(at))
			if final(path) {
				return
			}
			// An open hour this store never read (it was later than the clock
			// at start) holds what came before the restart: take it into RAM
			// first, so the write keeps both halves.
			if k.step == time.Hour {
				s.absorb(k, at, path)
			}
			data, err := s.encodeBucket(c, at, header{Version: version, Saved: now, Open: isOpen})
			if err != nil {
				errs = append(errs, err)
				return
			}
			if data == nil {
				return // the ring moved past it meanwhile
			}
			if err := writeAtomic(path, data); err != nil {
				errs = append(errs, err)
				return
			}
			// What this store wrote came from its RAM: it is never read back
			// into it.
			s.mu.Lock()
			s.taken[fileKey{k.dir, at.Unix()}] = true
			s.mu.Unlock()
			written++
		}
		for _, at := range shut {
			write(at, false)
		}
		for _, at := range filling {
			write(at, true)
		}

		entries, err := os.ReadDir(sub)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			name := e.Name()
			at, ok := k.parse(name)
			// A temp file is a write a power cut stopped; its rename never
			// happened, so nothing refers to it.
			stale := strings.Contains(name, ".gob.tmp")
			if stale || (ok && at.Before(keep)) {
				if err := os.Remove(filepath.Join(sub, name)); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return written, errors.Join(errs...)
}

// absorb takes an open file the store has not read into RAM; takeLocked
// skips one it has. A file it cannot read is simply written over: there is
// nothing in it to keep.
func (s *Store) absorb(k kind, at time.Time, path string) {
	bk, _, err := readBucket(path, at)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.takeLocked(k, bk)
}

// final says whether path holds a bucket written after it closed. A file
// that is missing, open, or unreadable is not final, and is written again.
func final(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hd header
	if err := gob.NewDecoder(bufio.NewReader(f)).Decode(&hd); err != nil {
		return false
	}
	return (hd.Version == 1 || hd.Version == version) && !hd.Open
}

// encodeBucket encodes one bucket under the read lock, so the lock is never
// held while waiting on flash. It returns nil if the bucket is gone.
func (s *Store) encodeBucket(c *coarse, at time.Time, hd header) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bk := c.get(at)
	if bk == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(hd); err != nil {
		return nil, err
	}
	if err := enc.Encode(bk); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeAtomic writes data beside path and renames it over path, so a power
// cut mid-write leaves either no file or the whole one.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	done = true
	// The rename is only durable once the directory is; a filesystem that
	// cannot sync a directory still has the file, so this is best effort.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// readBucket reads one bucket file and checks it holds the bucket its name
// says. The name is a whole hour or day, so this also checks the bucket
// starts on one.
func readBucket(path string, want time.Time) (*bucket, header, error) {
	var hd header
	f, err := os.Open(path)
	if err != nil {
		return nil, hd, err
	}
	defer f.Close()
	dec := gob.NewDecoder(bufio.NewReader(f))
	if err := dec.Decode(&hd); err != nil {
		return nil, hd, fmt.Errorf("header: %w", err)
	}
	var bk *bucket
	switch hd.Version {
	case version:
		bk = &bucket{}
		err = dec.Decode(bk)
	case 1:
		var v1 bucketV1
		if err = dec.Decode(&v1); err == nil {
			bk = v1.upgrade()
		}
	default:
		return nil, hd, fmt.Errorf("version %d, this reads 1 and %d", hd.Version, version)
	}
	if err != nil {
		// A file cut right after its header ends cleanly, and is as short
		// as any other truncation.
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, hd, err
	}
	bk.At = bk.At.UTC()
	if !bk.At.Equal(want) {
		return nil, hd, fmt.Errorf("holds %v, named for %v", bk.At, want)
	}
	return bk, hd, nil
}

// LoadDir loads the buckets SaveClosed and SaveOpen wrote, at start. A file
// that is damaged, of another version, misnamed, or later than the clock is
// skipped and named in the returned error, and every other file still
// loads: one bad file never empties the store. A router that starts before
// NTP must not turn its rings to a time it has not reached, so files later
// than its clock are set aside; once the clock is right, call LoadDir again,
// and it loads those (and only those) that are now in the past. Every file
// is read into RAM at most once, whether by LoadDir or by a save. A missing
// directory is a first start, not an error. An open hour keeps filling. A
// day is read from its own file only if that file is final; otherwise,
// including an open day file older code wrote, its hours rebuild it.
func LoadDir(s *Store, dir string) error {
	type file struct {
		k    kind
		at   time.Time
		path string
	}
	var errs []error
	var files []file
	s.mu.Lock()
	again := s.loaded
	s.loaded = true
	limit := s.now().UTC().Add(skew)
	if again {
		for fk := range s.future {
			for _, k := range kinds {
				if k.dir == fk.dir {
					at := time.Unix(fk.at, 0).UTC()
					files = append(files, file{k, at, filepath.Join(dir, k.dir, k.name(at))})
				}
			}
		}
	}
	s.mu.Unlock()
	if !again {
		for _, k := range kinds {
			sub := filepath.Join(dir, k.dir)
			entries, err := os.ReadDir(sub)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, err)
				}
				continue
			}
			for _, e := range entries {
				if at, ok := k.parse(e.Name()); ok {
					files = append(files, file{k, at, filepath.Join(sub, e.Name())})
				}
			}
		}
	}
	// Days before hours, so an hour knows whether its day came from a final
	// file; newest first, so a ring is placed before anything older is
	// offered to it and a bucket it no longer reaches is skipped, not parked.
	sort.Slice(files, func(i, j int) bool {
		if files[i].k.step != files[j].k.step {
			return files[i].k.step > files[j].k.step
		}
		return files[i].at.After(files[j].at)
	})
	type read struct {
		k  kind
		bk *bucket
	}
	var got []read
	var later []fileKey
	for _, f := range files {
		if f.at.After(limit) {
			later = append(later, fileKey{f.k.dir, f.at.Unix()})
			errs = append(errs, fmt.Errorf("%s: starts at %v, after the clock", f.path, f.at))
			continue
		}
		bk, hd, err := readBucket(f.path, f.at)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) || !again {
				errs = append(errs, fmt.Errorf("%s: %w", f.path, err))
			}
			continue
		}
		if f.k.step == day && hd.Open {
			continue // rebuilt from its hours
		}
		got = append(got, read{f.k, bk})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, fk := range later {
		s.future[fk] = true
	}
	for _, r := range got {
		s.takeLocked(r.k, r.bk)
	}
	s.sweep(s.now().UTC())
	return errors.Join(errs...)
}
