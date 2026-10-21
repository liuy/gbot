package providers

import (
	"strconv"
	"testing"
	"time"
)

func TestClampNumResults(t *testing.T) {
	cases := []struct {
		name               string
		value, def, maxVal int
		want               int
	}{
		{"zero selects default", 0, 5, 20, 5},
		{"value in range kept", 3, 5, 20, 3},
		{"value above max clamped", 25, 5, 20, 20},
		{"negative clamps up to 1", -5, 5, 20, 1},
		{"one stays one", 1, 5, 20, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampNumResults(c.value, c.def, c.maxVal); got != c.want {
				t.Errorf("clampNumResults(%d, %d, %d) = %d, want %d", c.value, c.def, c.maxVal, got, c.want)
			}
		})
	}
}

// unknownAgeSeconds is the exact sentinel dateToAgeSeconds returns for
// missing/unparseable dates.
const unknownAgeSeconds int64 = 0

func TestDateToAgeSeconds(t *testing.T) {
	for _, s := range []string{"", "not-a-date"} {
		if got := dateToAgeSeconds(s); got != unknownAgeSeconds {
			t.Errorf("dateToAgeSeconds(%q) = %d, want %d (unknown)", s, got, unknownAgeSeconds)
		}
	}

	parsed, err := time.Parse("2006-01-02", "2019-06-01")
	if err != nil {
		t.Fatalf("parse reference date: %v", err)
	}
	want := int64(time.Since(parsed).Seconds())
	got := dateToAgeSeconds("2019-06-01")
	diff := got - want
	if diff < -5 || diff > 5 {
		t.Errorf("dateToAgeSeconds(2019-06-01) = %d, want within ±5s of %d", got, want)
	}
}

// The sub-second cases pin Math.floor semantics against Go's truncate-toward-
// zero int64 conversion: ±500ms targets sit halfway between the two candidate
// integer results, so only correct flooring passes.
func TestDateToAgeSeconds_FloorSemantics(t *testing.T) {
	const subSecondPastAge int64 = 0
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	recent := time.Now().Add(-500 * time.Millisecond)
	if got := dateToAgeSeconds(recent.Format(time.RFC3339Nano)); got != subSecondPastAge {
		t.Errorf("sub-second past age = %d, want %d (floor, not trunc-decrement)", got, subSecondPastAge)
	}

	const subSecondFutureAge int64 = -1
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	soon := time.Now().Add(500 * time.Millisecond)
	if got := dateToAgeSeconds(soon.Format(time.RFC3339Nano)); got != subSecondFutureAge {
		t.Errorf("sub-second future age = %d, want %d (floor rounds down past zero)", got, subSecondFutureAge)
	}

	// 10h+500ms in the future: floor lands strictly below -36000, while
	// truncation without the decrement would return exactly -36000.
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	far := time.Now().Add(10*time.Hour + 500*time.Millisecond)
	if got := dateToAgeSeconds(far.Format(time.RFC3339Nano)); got >= -36000 {
		t.Errorf("far future age = %d, want < -36000 (fractional part must floor below)", got)
	}
}

func TestClassifyProviderHttpError(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		status     int
		wantMsg    string
		wantStatus int
		rateLimit  bool
		authError  bool
		wantNil    bool
	}{
		{
			name:       "credit body beats status",
			body:       "Your credits exhausted",
			status:     429,
			wantMsg:    "credits exhausted",
			wantStatus: 429,
			rateLimit:  true,
		},
		{
			name:       "quota body at 200 is not rate limit",
			body:       "quota exceeded",
			status:     200,
			wantMsg:    "credits exhausted",
			wantStatus: 200,
		},
		{
			name:       "402",
			body:       "payment required",
			status:     402,
			wantMsg:    "402 credits exhausted",
			wantStatus: 402,
		},
		{
			name:       "401",
			body:       "no auth",
			status:     401,
			wantMsg:    "401 unauthorized",
			wantStatus: 401,
			authError:  true,
		},
		{
			name:       "403",
			body:       "denied",
			status:     403,
			wantMsg:    "403 forbidden",
			wantStatus: 403,
			authError:  true,
		},
		{
			name:    "unknown signal returns nil",
			body:    "upstream busy",
			status:  500,
			wantNil: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spe := classifyProviderHttpError("tavily", c.status, c.body)
			if c.wantNil {
				if spe != nil {
					t.Fatalf("classifyProviderHttpError = %v, want nil", spe)
				}
				return
			}
			if spe == nil {
				t.Fatal("classifyProviderHttpError = nil, want an error")
			}
			wantErr := "tavily: " + c.wantMsg + " (status " + strconv.Itoa(c.wantStatus) + ")"
			if spe.Error() != wantErr {
				t.Errorf("Error() = %q, want %q", spe.Error(), wantErr)
			}
			if spe.Status != c.wantStatus {
				t.Errorf("Status = %d, want %d", spe.Status, c.wantStatus)
			}
			if spe.IsRateLimit() != c.rateLimit {
				t.Errorf("IsRateLimit() = %v, want %v", spe.IsRateLimit(), c.rateLimit)
			}
			if spe.IsAuthError() != c.authError {
				t.Errorf("IsAuthError() = %v, want %v", spe.IsAuthError(), c.authError)
			}
		})
	}
}
