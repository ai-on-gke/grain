package granulefile

import (
	"testing"
)

func TestSettingEnvLookup(t *testing.T) {
	for _, tc := range []struct {
		key     string
		wantEnv string
		wantOk  bool
	}{
		{"cpus", "CHV_CPUS", true},
		{"CPUS", "CHV_CPUS", true},
		{"vcpus", "CHV_VCPUS", true},
		{"memory-mb", "CHV_MEMORY_MB", true},
		{"disk-mode", "CHV_DISK_MODE", true},
		{"storage-backend", "CHV_STORAGE_BACKEND", true},
		{"extent-size-mb", "CHV_EXTENT_SIZE_MB", true},
		{"disk-num-queues", "CHV_DISK_NUM_QUEUES", true},
		{"num-queues", "CHV_DISK_NUM_QUEUES", true},
		{"disk-iothreads", "CHV_DISK_IOTHREADS", true},
		{"iothreads", "CHV_DISK_IOTHREADS", true},
		{"disk-io-engine", "CHV_DISK_IO_ENGINE", true},
		{"io-engine", "CHV_DISK_IO_ENGINE", true},
		{"nonexistent", "", false},
	} {
		env, ok := SettingEnv(tc.key)
		if ok != tc.wantOk {
			t.Errorf("SettingEnv(%q) ok = %v, want %v", tc.key, ok, tc.wantOk)
		}
		if env != tc.wantEnv {
			t.Errorf("SettingEnv(%q) = %q, want %q", tc.key, env, tc.wantEnv)
		}
	}
}

func TestValueCheckers(t *testing.T) {
	if err := positiveInt("4"); err != nil {
		t.Errorf("positiveInt(4) failed: %v", err)
	}
	if err := positiveInt("0"); err == nil {
		t.Error("positiveInt(0) expected error, got nil")
	}
	if err := positiveInt("-1"); err == nil {
		t.Error("positiveInt(-1) expected error, got nil")
	}
	if err := positiveInt("abc"); err == nil {
		t.Error("positiveInt(abc) expected error, got nil")
	}

	if err := wholeInt("0"); err != nil {
		t.Errorf("wholeInt(0) failed: %v", err)
	}
	if err := wholeInt("-1"); err == nil {
		t.Error("wholeInt(-1) expected error, got nil")
	}

	if err := nonEmpty("val"); err != nil {
		t.Errorf("nonEmpty(val) failed: %v", err)
	}
	if err := nonEmpty("  "); err == nil {
		t.Error("nonEmpty(  ) expected error, got nil")
	}

	if err := duration("30s"); err != nil {
		t.Errorf("duration(30s) failed: %v", err)
	}
	if err := duration("invalid"); err == nil {
		t.Error("duration(invalid) expected error, got nil")
	}

	for _, good := range []string{"on", "off", "true", "false", "1", "0", "ON", "TRUE"} {
		if err := onOff(good); err != nil {
			t.Errorf("onOff(%q) failed: %v", good, err)
		}
	}
	for _, bad := range []string{"maybe", "2", "yes", "no"} {
		if err := onOff(bad); err == nil {
			t.Errorf("onOff(%q) expected error, got nil", bad)
		}
	}

	if !on("true") || !on("on") || !on("1") {
		t.Error("on(...) expected true for true, on, 1")
	}
	if on("off") || on("false") || on("0") {
		t.Error("on(...) expected false for off, false, 0")
	}

	if err := serviceName("my-service.1"); err != nil {
		t.Errorf("serviceName(my-service.1) failed: %v", err)
	}
	if err := serviceName("Invalid/Service"); err == nil {
		t.Error("serviceName(Invalid/Service) expected error, got nil")
	}
}

func TestSettingKeysAndOptionList(t *testing.T) {
	keys := settingKeys()
	if len(keys) == 0 {
		t.Error("settingKeys() returned empty slice")
	}
	optStr := optionList(wrapOptions)
	if optStr == "" {
		t.Error("optionList(wrapOptions) returned empty string")
	}
}
