// These tests execute the deployed Lua source against fake LuatOS hardware APIs.
// They do not send SMS, access serial devices or require a Lua installation.
package firmwaretest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

type disk struct {
	kv    map[string]any
	files map[string]string
	fail  bool
}
type harness struct {
	L      *lua.LState
	t      *testing.T
	disk   *disk
	output []map[string]any
	input  string
	sends  int
	ticks  int64
}

func native(v lua.LValue) any {
	switch x := v.(type) {
	case *lua.LTable:
		if x.Len() > 0 {
			a := []any{}
			for i := 1; i <= x.Len(); i++ {
				a = append(a, native(x.RawGetInt(i)))
			}
			return a
		}
		m := map[string]any{}
		x.ForEach(func(k, v lua.LValue) { m[k.String()] = native(v) })
		return m
	case lua.LString:
		return string(x)
	case lua.LNumber:
		return float64(x)
	case lua.LBool:
		return bool(x)
	default:
		return nil
	}
}
func value(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(x)
	case bool:
		return lua.LBool(x)
	case float64:
		return lua.LNumber(x)
	case int:
		return lua.LNumber(x)
	case int64:
		return lua.LNumber(x)
	case []any:
		a := L.NewTable()
		for _, v := range x {
			a.Append(value(L, v))
		}
		return a
	case map[string]any:
		a := L.NewTable()
		for k, v := range x {
			a.RawSetString(k, value(L, v))
		}
		return a
	default:
		panic("unsupported value")
	}
}
func newHarness(t *testing.T, d *disk) *harness {
	t.Helper()
	if d == nil {
		d = &disk{kv: map[string]any{}, files: map[string]string{}}
	}
	h := &harness{L: lua.NewState(), t: t, disk: d}
	L := h.L
	t.Cleanup(L.Close)
	set := func(name string, functions map[string]lua.LGFunction) *lua.LTable {
		tab := L.NewTable()
		L.SetFuncs(tab, functions)
		L.SetGlobal(name, tab)
		return tab
	}
	constant := func(v any) lua.LGFunction { return func(L *lua.LState) int { L.Push(value(L, v)); return 1 } }
	set("json", map[string]lua.LGFunction{"encode": func(L *lua.LState) int {
		b, e := json.Marshal(native(L.Get(1)))
		if e != nil {
			L.RaiseError("json")
		}
		L.Push(lua.LString(b))
		return 1
	}, "decode": func(L *lua.LState) int {
		var v any
		if e := json.Unmarshal([]byte(L.CheckString(1)), &v); e != nil {
			L.RaiseError("invalid json")
		}
		L.Push(value(L, v))
		return 1
	}})
	set("fskv", map[string]lua.LGFunction{"init": constant(true), "get": func(L *lua.LState) int { L.Push(value(L, d.kv[L.CheckString(1)])); return 1 }, "set": func(L *lua.LState) int {
		if d.fail {
			L.Push(lua.LFalse)
			return 1
		}
		d.kv[L.CheckString(1)] = native(L.Get(2))
		L.Push(lua.LTrue)
		return 1
	}, "del": func(L *lua.LState) int { delete(d.kv, L.CheckString(1)); L.Push(lua.LTrue); return 1 }})
	set("io", map[string]lua.LGFunction{"open": func(L *lua.LState) int {
		path, mode := L.CheckString(1), L.CheckString(2)
		text, exists := d.files[path]
		writing := strings.ContainsAny(mode, "wa")
		if (!exists && !writing) || d.fail {
			L.Push(lua.LNil)
			return 1
		}
		if strings.Contains(mode, "w") {
			text = ""
		}
		f := L.NewTable()
		L.SetFuncs(f, map[string]lua.LGFunction{"write": func(L *lua.LState) int {
			if d.fail {
				L.Push(lua.LNil)
				return 1
			}
			for i := 2; i <= L.GetTop(); i++ {
				text += L.CheckString(i)
			}
			L.Push(f)
			return 1
		}, "close": func(L *lua.LState) int {
			if d.fail {
				L.Push(lua.LNil)
				return 1
			}
			if writing {
				d.files[path] = text
			}
			L.Push(lua.LTrue)
			return 1
		}, "lines": func(L *lua.LState) int {
			lines := strings.Split(text, "\n")
			i := 0
			L.Push(L.NewFunction(func(L *lua.LState) int {
				if i >= len(lines) {
					return 0
				}
				L.Push(lua.LString(lines[i]))
				i++
				return 1
			}))
			return 1
		}})
		L.Push(f)
		return 1
	}})
	L.GetGlobal("os").(*lua.LTable).RawSetString("rename", L.NewFunction(func(L *lua.LState) int {
		if d.fail {
			L.Push(lua.LNil)
			return 1
		}
		a, b := L.CheckString(1), L.CheckString(2)
		d.files[b] = d.files[a]
		delete(d.files, a)
		L.Push(lua.LTrue)
		return 1
	}))
	set("mobile", map[string]lua.LGFunction{"imei": constant("device-1"), "number": constant(nil), "iccid": constant("sim-1"), "status": constant(1), "csq": constant(27), "rsrp": constant(-86)})
	set("rtos", map[string]lua.LGFunction{"bsp": constant("Air780EPM"), "version": constant("V2044")})
	set("mcu", map[string]lua.LGFunction{"ticks": func(L *lua.LState) int { L.Push(lua.LNumber(h.ticks)); return 1 }})
	set("log", map[string]lua.LGFunction{"info": func(*lua.LState) int { return 0 }})
	u := set("uart", map[string]lua.LGFunction{"setup": func(*lua.LState) int { return 0 }, "on": func(L *lua.LState) int { L.SetGlobal("_receive", L.Get(3)); return 0 }, "read": func(L *lua.LState) int { L.Push(lua.LString(h.input)); h.input = ""; return 1 }, "write": func(L *lua.LState) int {
		var out map[string]any
		if e := json.Unmarshal([]byte(strings.TrimSpace(L.CheckString(2))), &out); e != nil {
			t.Fatal(e)
		}
		h.output = append(h.output, out)
		return 0
	}})
	u.RawSetString("VUART_0", lua.LNumber(32))
	set("sms", map[string]lua.LGFunction{"autoLong": func(L *lua.LState) int {
		if L.CheckBool(1) {
			t.Fatal("automatic merge enabled")
		}
		L.Push(lua.LFalse)
		return 1
	}, "setNewSmsCb": func(L *lua.LState) int { L.SetGlobal("_sms", L.Get(1)); return 0 }, "send": func(L *lua.LState) int {
		h.sends++
		if L.ToBool(3) != strings.HasPrefix(L.CheckString(1), "+") {
			t.Fatal("incorrect international/service-number addressing mode")
		}
		L.Push(lua.LTrue)
		return 1
	}})
	sys := L.NewTable()
	L.SetFuncs(sys, map[string]lua.LGFunction{"run": func(*lua.LState) int { return 0 }, "subscribe": func(L *lua.LState) int { L.SetGlobal("_"+L.CheckString(1), L.Get(2)); return 0 }, "timerLoopStart": func(L *lua.LState) int { L.SetGlobal("_timer", L.Get(1)); return 0 }})
	L.PreloadModule("sys", func(L *lua.LState) int { L.Push(sys); return 1 })
	L.SetGlobal("PROJECT", lua.LString("test"))
	L.SetGlobal("VERSION", lua.LString("0.2.0"))
	source, err := os.ReadFile(filepath.Join("..", "..", "sms-bridge", "gateway.lua"))
	if err != nil {
		t.Fatal(err)
	}
	if err = L.DoString(string(source)); err != nil {
		t.Fatal(err)
	}
	return h
}
func (h *harness) call(name string, args ...lua.LValue) {
	h.t.Helper()
	if e := h.L.CallByParam(lua.P{Fn: h.L.GetGlobal(name), NRet: 0, Protect: true}, args...); e != nil {
		h.t.Fatal(e)
	}
}
func (h *harness) request(q map[string]any) map[string]any {
	h.t.Helper()
	b, _ := json.Marshal(q)
	h.input = string(b) + "\n"
	h.call("_receive", lua.LNumber(32), lua.LNumber(len(h.input)))
	if len(h.output) == 0 {
		h.t.Fatal("no response")
	}
	return h.output[len(h.output)-1]
}
func (h *harness) receive(text string, seq int) {
	h.t.Helper()
	meta := value(h.L, map[string]any{"maxNum": 2, "seqNum": seq, "refNum": 7})
	h.call("_sms", lua.LString("50000"), lua.LString(text), meta)
}
func TestBootNoAutomaticSendAndStatus(t *testing.T) {
	h := newHarness(t, nil)
	if h.sends != 0 {
		t.Fatal("automatic SMS")
	}
	s := h.request(map[string]any{"cmd": "status"})
	if s["protocol"] != float64(2) || s["durable"] != true || s["device_id"] != "device-1" {
		t.Fatal(s)
	}
}
func TestJournalSurvivesRestartAndAck(t *testing.T) {
	h := newHarness(t, nil)
	h.receive("分片一", 1)
	e := h.output[len(h.output)-1]
	h2 := newHarness(t, h.disk)
	in := h2.request(map[string]any{"cmd": "inbox"})
	items := in["messages"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["text"] != "分片一" {
		t.Fatal(in)
	}
	h2.request(map[string]any{"cmd": "ack", "message_id": e["message_id"], "boot_id": e["boot_id"]})
	h3 := newHarness(t, h.disk)
	if h3.request(map[string]any{"cmd": "inbox"})["messages"] != nil {
		t.Fatal("ack not persisted")
	}
}
func TestSendJournalPreventsDuplicateAfterReboot(t *testing.T) {
	h := newHarness(t, nil)
	q := map[string]any{"cmd": "sms_send", "id": "unique", "to": "+12025550100", "text": "test"}
	h.request(q)
	if h.sends != 1 {
		t.Fatal(h.sends)
	}
	h2 := newHarness(t, h.disk)
	r := h2.request(q)
	if h2.sends != 0 || r["state"] != "unknown" {
		t.Fatal("replayed uncertain send", r)
	}
	h.call("_SMS_SENT", lua.LTrue, lua.LNumber(0), lua.LString("ok"), lua.LNumber(1))
	r = h.request(q)
	if h.sends != 1 || r["submitted"] != true {
		t.Fatal(r)
	}
}
func TestMalformedStreamingAndPersistenceFailure(t *testing.T) {
	h := newHarness(t, nil)
	h.input = `{"cmd":"sta`
	h.call("_receive", lua.LNumber(32), lua.LNumber(len(h.input)))
	h.input = "tus\"}\n"
	h.call("_receive", lua.LNumber(32), lua.LNumber(len(h.input)))
	if h.output[len(h.output)-1]["event"] != "status" {
		t.Fatal(h.output)
	}
	h.input = "garbage\n"
	h.call("_receive", lua.LNumber(32), lua.LNumber(8))
	if h.output[len(h.output)-1]["event"] != "error" {
		t.Fatal("invalid JSON accepted")
	}
	h.disk.fail = true
	r := h.request(map[string]any{"cmd": "sms_send", "id": "new", "to": "50000", "text": "101"})
	if h.sends != 0 || r["event"] != "error" {
		t.Fatal("send despite journal failure")
	}
}
func TestLateAckAndBoundedInbox(t *testing.T) {
	h := newHarness(t, nil)
	h.receive("first", 1)
	first := h.output[len(h.output)-1]
	h.request(map[string]any{"cmd": "ack", "message_id": first["message_id"], "boot_id": "wrong"})
	if h.request(map[string]any{"cmd": "status"})["inbox_count"] != float64(1) {
		t.Fatal("wrong boot ack accepted")
	}
	for i := 0; i < 64; i++ {
		h.receive("next", 2)
	}
	if h.output[len(h.output)-1]["event"] != "overflow" {
		t.Fatal("unbounded inbox")
	}
}
func TestSendTimeoutDoesNotFreeSlotOrResend(t *testing.T) {
	h := newHarness(t, nil)
	h.request(map[string]any{"cmd": "sms_send", "id": "a", "to": "50000", "text": "101"})
	h.ticks = 100000
	h.call("_timer")
	h.request(map[string]any{"cmd": "sms_send", "id": "b", "to": "50000", "text": "101"})
	if h.sends != 1 {
		t.Fatal("new send after unknown outcome")
	}
}
