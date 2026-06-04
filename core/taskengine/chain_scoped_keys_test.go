package taskengine

import (
	"errors"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestParseTaskStatusKey(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		wantChainID int64
		wantStatus  avsproto.TaskStatus
		wantTaskID  string
		wantLegacy  bool
		wantErr     bool
	}{
		{
			name:       "legacy enabled",
			key:        "t:a:01JG2FE5MDVKBPHEG0PEYSDKAC",
			wantStatus: avsproto.TaskStatus_Enabled,
			wantTaskID: "01JG2FE5MDVKBPHEG0PEYSDKAC",
			wantLegacy: true,
		},
		{
			name:        "chain-scoped enabled",
			key:         "t:11155111:a:01JG2FE5MDVKBPHEG0PEYSDKAC",
			wantChainID: 11155111,
			wantStatus:  avsproto.TaskStatus_Enabled,
			wantTaskID:  "01JG2FE5MDVKBPHEG0PEYSDKAC",
		},
		{
			name:        "chain-scoped completed on Base",
			key:         "t:8453:c:01JG2FE5MDVKBPHEG0PEYSDKAC",
			wantChainID: 8453,
			wantStatus:  avsproto.TaskStatus_Completed,
			wantTaskID:  "01JG2FE5MDVKBPHEG0PEYSDKAC",
		},
		{name: "not a task key", key: "history:foo:bar", wantErr: true},
		{name: "malformed", key: "t:", wantErr: true},
		{name: "bad chain id", key: "t:notanumber:a:01JG2FE5", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := ParseTaskStatusKey([]byte(c.key))
			switch {
			case c.wantErr && !c.wantLegacy:
				if err == nil || errors.Is(err, ErrLegacyKey) {
					t.Fatalf("want hard error, got %v", err)
				}
				return
			case c.wantLegacy:
				if !errors.Is(err, ErrLegacyKey) {
					t.Fatalf("want ErrLegacyKey, got %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if parsed.ChainID != c.wantChainID {
				t.Errorf("chain_id: want %d, got %d", c.wantChainID, parsed.ChainID)
			}
			if parsed.Status != c.wantStatus {
				t.Errorf("status: want %v, got %v", c.wantStatus, parsed.Status)
			}
			if parsed.TaskID != c.wantTaskID {
				t.Errorf("task_id: want %q, got %q", c.wantTaskID, parsed.TaskID)
			}
		})
	}
}

func TestParseExecutionKey(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		wantChainID int64
		wantTaskID  string
		wantExecID  string
		wantLegacy  bool
		wantErr     bool
	}{
		{
			name:       "legacy",
			key:        "history:01JG2FE5MDVKBPHEG0PEYSDKAC:01JG2FE5MFKTH0754RGF2DMVY7",
			wantTaskID: "01JG2FE5MDVKBPHEG0PEYSDKAC",
			wantExecID: "01JG2FE5MFKTH0754RGF2DMVY7",
			wantLegacy: true,
		},
		{
			name:        "chain-scoped sepolia",
			key:         "history:11155111:01JG2FE5MDVKBPHEG0PEYSDKAC:01JG2FE5MFKTH0754RGF2DMVY7",
			wantChainID: 11155111,
			wantTaskID:  "01JG2FE5MDVKBPHEG0PEYSDKAC",
			wantExecID:  "01JG2FE5MFKTH0754RGF2DMVY7",
		},
		{name: "not an execution key", key: "t:a:01JG2FE5", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := ParseExecutionKey([]byte(c.key))
			switch {
			case c.wantErr && !c.wantLegacy:
				if err == nil || errors.Is(err, ErrLegacyKey) {
					t.Fatalf("want hard error, got %v", err)
				}
				return
			case c.wantLegacy:
				if !errors.Is(err, ErrLegacyKey) {
					t.Fatalf("want ErrLegacyKey, got %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if parsed.ChainID != c.wantChainID {
				t.Errorf("chain_id: want %d, got %d", c.wantChainID, parsed.ChainID)
			}
			if parsed.TaskID != c.wantTaskID {
				t.Errorf("task_id: want %q, got %q", c.wantTaskID, parsed.TaskID)
			}
			if parsed.ExecutionID != c.wantExecID {
				t.Errorf("exec_id: want %q, got %q", c.wantExecID, parsed.ExecutionID)
			}
		})
	}
}

func TestParseUserTaskKey(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		wantChainID int64
		wantOwner   string
		wantWallet  string
		wantTaskID  string
		wantLegacy  bool
		wantErr     bool
	}{
		{
			name:       "legacy",
			key:        "u:0xd7050816337a3f8f690f8083b5ff8019d50c0e50:0x415f09526f25d6520d471890abf0953b0505313d:01JMN2JHAGXTNSY46KH0KYY0MZ",
			wantOwner:  "0xd7050816337a3f8f690f8083b5ff8019d50c0e50",
			wantWallet: "0x415f09526f25d6520d471890abf0953b0505313d",
			wantTaskID: "01JMN2JHAGXTNSY46KH0KYY0MZ",
			wantLegacy: true,
		},
		{
			name:        "chain-scoped sepolia",
			key:         "u:11155111:0xd7050816337a3f8f690f8083b5ff8019d50c0e50:0x415f09526f25d6520d471890abf0953b0505313d:01JMN2JHAGXTNSY46KH0KYY0MZ",
			wantChainID: 11155111,
			wantOwner:   "0xd7050816337a3f8f690f8083b5ff8019d50c0e50",
			wantWallet:  "0x415f09526f25d6520d471890abf0953b0505313d",
			wantTaskID:  "01JMN2JHAGXTNSY46KH0KYY0MZ",
		},
		{name: "malformed", key: "u:onlyone", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := ParseUserTaskKey([]byte(c.key))
			switch {
			case c.wantErr && !c.wantLegacy:
				if err == nil || errors.Is(err, ErrLegacyKey) {
					t.Fatalf("want hard error, got %v", err)
				}
				return
			case c.wantLegacy:
				if !errors.Is(err, ErrLegacyKey) {
					t.Fatalf("want ErrLegacyKey, got %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if parsed.ChainID != c.wantChainID {
				t.Errorf("chain_id: want %d, got %d", c.wantChainID, parsed.ChainID)
			}
			if parsed.Owner != c.wantOwner {
				t.Errorf("owner: want %q, got %q", c.wantOwner, parsed.Owner)
			}
			if parsed.SmartWalletAddress != c.wantWallet {
				t.Errorf("wallet: want %q, got %q", c.wantWallet, parsed.SmartWalletAddress)
			}
			if parsed.TaskID != c.wantTaskID {
				t.Errorf("task_id: want %q, got %q", c.wantTaskID, parsed.TaskID)
			}
		})
	}
}

func TestIsChainScopedKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"t:a:01JG2FE5MDVKBPHEG0PEYSDKAC", false},
		{"t:1:a:01JG2FE5MDVKBPHEG0PEYSDKAC", true},
		{"u:0xowner:0xwallet:01JG2FE5", false},
		{"u:1:0xowner:0xwallet:01JG2FE5", true},
		{"history:01JG:01JG", false},
		{"history:1:01JG:01JG", true},
		{"secret:_:0xowner:_:mysecret", false}, // not a chain-scoped prefix
		{"unknown:foo", false},
	}
	for _, c := range cases {
		got := IsChainScopedKey([]byte(c.key))
		if got != c.want {
			t.Errorf("IsChainScopedKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}
