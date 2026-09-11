package adminreset

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeResetter struct {
	password string
	err      error
}

func (f *fakeResetter) ResetAdministratorPassword(_ context.Context, password string) error {
	f.password = password
	return f.err
}

func TestRunPromptsTwiceAndNeverWritesPassword(t *testing.T) {
	service := &fakeResetter{}
	values := []string{"new administrator password", "new administrator password"}
	output := new(bytes.Buffer)
	if err := Run(context.Background(), service, output, func() (string, error) {
		value := values[0]
		values = values[1:]
		return value, nil
	}); err != nil {
		t.Fatal(err)
	}
	if service.password != "new administrator password" {
		t.Fatalf("reset password = %q", service.password)
	}
	if strings.Contains(output.String(), service.password) {
		t.Fatalf("password leaked in output: %q", output.String())
	}
	if !strings.Contains(output.String(), "successfully") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunRejectsMismatchWithoutReset(t *testing.T) {
	service := &fakeResetter{}
	values := []string{"first password value", "second password value"}
	if err := Run(context.Background(), service, nil, func() (string, error) {
		value := values[0]
		values = values[1:]
		return value, nil
	}); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("Run error = %v, want ErrPasswordMismatch", err)
	}
	if service.password != "" {
		t.Fatal("resetter called after password mismatch")
	}
}

func TestRunDoesNotExposeResetterError(t *testing.T) {
	service := &fakeResetter{err: errors.New("database path /private/secret.sqlite password=leak")}
	values := []string{"new administrator password", "new administrator password"}
	output := new(bytes.Buffer)
	err := Run(context.Background(), service, output, func() (string, error) {
		value := values[0]
		values = values[1:]
		return value, nil
	})
	if !errors.Is(err, ErrResetFailed) {
		t.Fatalf("Run error = %v, want ErrResetFailed", err)
	}
	if strings.Contains(err.Error(), "private/secret.sqlite") || strings.Contains(output.String(), "private/secret.sqlite") {
		t.Fatalf("reset error leaked dependency detail: %v / %q", err, output.String())
	}
}
