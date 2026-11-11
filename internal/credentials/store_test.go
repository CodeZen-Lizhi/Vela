package credentials

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestMigrationAndReopen 验证一次性迁移、未设置标记、重启重读、密文及文件权限。
func TestMigrationAndReopen(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	s, err := New(dir, func(name string) ([]byte, error) {
		calls++
		if name == "gateway-access-key" {
			return []byte("test-legacy-secret"), nil
		}
		return nil, ErrNotFound
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := s.Get("gateway-access-key"); err != nil || string(value) != "test-legacy-secret" {
		t.Fatal("旧凭据未迁移")
	}
	if _, err := s.Get("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("旧存储调用次数不正确")
	}
	s, err = New(dir, func(string) ([]byte, error) {
		t.Fatal("本地已记录的凭据不应访问旧存储")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := s.Get("gateway-access-key"); err != nil || string(value) != "test-legacy-secret" {
		t.Fatal("重启读取失败")
	}
	if _, err := s.Get("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Set("gateway-access-key", []byte("test-new-secret")); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"master.key", "secrets.enc"} {
		path := filepath.Join(dir, "credentials", file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("test-legacy-secret")) || bytes.Contains(data, []byte("test-new-secret")) {
			t.Fatal("文件含明文凭据")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("凭据文件权限不正确")
		}
	}
	info, err := os.Stat(filepath.Join(dir, "credentials"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("凭据目录权限不正确")
	}
}

// TestFailedMigrationRetry 验证拒绝授权时不持久化错误或覆盖旧凭据，之后可以重试迁移。
func TestFailedMigrationRetry(t *testing.T) {
	dir := t.TempDir()
	denied := true
	s, err := New(dir, func(string) ([]byte, error) {
		if denied {
			return nil, errors.New("test denied")
		}
		return []byte("test-secret"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("gateway-access-key"); err == nil {
		t.Fatal("迁移失败应报错")
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials", "secrets.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("失败不应写入文件")
	}
	denied = false
	if _, err := s.Get("gateway-access-key"); err != nil {
		t.Fatal(err)
	}
}

// TestCorruptionPreservesData 验证缺失、错误主密钥与损坏密文均拒绝读取和覆盖，恢复原文件后可读。
func TestCorruptionPreservesData(t *testing.T) {
	for _, damage := range []string{"missing-key", "wrong-key", "corrupt-data", "missing-data"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Set("key", []byte("test-secret")); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(dir, "credentials", "master.key")
			dataPath := filepath.Join(dir, "credentials", "secrets.enc")
			key, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := os.ReadFile(dataPath)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "missing-key":
				err = os.Remove(keyPath)
			case "wrong-key":
				err = os.WriteFile(keyPath, bytes.Repeat([]byte{1}, 32), 0600)
			case "corrupt-data":
				err = os.WriteFile(dataPath, []byte("invalid"), 0600)
			case "missing-data":
				err = os.Remove(dataPath)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(dataPath)
			if err != nil && !(damage == "missing-data" && errors.Is(err, os.ErrNotExist)) {
				t.Fatal(err)
			}
			if _, err := s.Get("key"); err == nil {
				t.Fatal("损坏后不应读取成功")
			}
			if err := s.Set("key", []byte("replacement")); err == nil {
				t.Fatal("损坏后不应覆盖")
			}
			after, err := os.ReadFile(dataPath)
			if (err != nil && !(damage == "missing-data" && errors.Is(err, os.ErrNotExist))) || !bytes.Equal(before, after) {
				t.Fatal("失败改动了密文")
			}
			if err := os.WriteFile(keyPath, key, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dataPath, sealed, 0600); err != nil {
				t.Fatal(err)
			}
			if value, err := s.Get("key"); err != nil || string(value) != "test-secret" {
				t.Fatal("恢复后无法读取")
			}
		})
	}
}

// TestConcurrentEntries 验证并发保存不同凭据时不丢条目。
func TestConcurrentEntries(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, name := range []string{"gateway-access-key", "provider-master-key"} {
		wg.Go(func() {
			if err := s.Set(name, []byte(name)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, name := range []string{"gateway-access-key", "provider-master-key"} {
		if value, err := s.Get(name); err != nil || string(value) != name {
			t.Fatal("并发保存丢失凭据")
		}
	}
}

// TestFailedSaveKeepsOldFile 验证保存失败不覆盖旧凭据，且拒绝将密文路径的符号链接当成文件。
func TestFailedSaveKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("key", []byte("old-secret")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials", "secrets.enc")
	old := path + ".old"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, path); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("key", []byte("new-secret")); err == nil {
		t.Fatal("符号链接应被拒绝")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, path); err != nil {
		t.Fatal(err)
	}
	if value, err := s.Get("key"); err != nil || string(value) != "old-secret" {
		t.Fatal("失败后原凭据丢失")
	}
}
