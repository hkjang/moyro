package digest

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hkjang/moyro/server/internal/store"
)

const digestTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

// capturingSender stands in for SMTP only — the network boundary. Everything
// between tick() and the rendered body is the real production path.
type capturingSender struct {
	sent []capturedMail
}

type capturedMail struct {
	To, Subject, HTML, Text string
}

func (c *capturingSender) Send(_ context.Context, to, subject, htmlBody, textBody string) error {
	c.sent = append(c.sent, capturedMail{To: to, Subject: subject, HTML: htmlBody, Text: textBody})
	return nil
}

// A mention excerpt is cut to a fixed length before it is rendered into the
// daily digest email. The cut used to be a byte slice (`msg[:160]`), and the
// digest templates are Korean-facing: one Hangul syllable is three UTF-8
// bytes, so the byte at offset 160 lands in the middle of a character far
// more often than not. The excerpt that reaches the recipient is then not
// valid UTF-8 and the mail client renders a replacement character.
//
// The repository already treats this as a solved problem everywhere else a
// string is shortened — knowledge.truncateRunes counts runes, and both
// scheduled.truncateError and webhooks.truncateDeliveryError walk back to a
// valid boundary after a byte cut. The digest excerpt was the one caller that
// did not.
//
// This test drives the real worker: production candidate SQL, production
// detail SQL, production template render. Only Send is substituted, because
// that is the SMTP hop.
func TestDigestExcerptStaysValidUTF8(t *testing.T) {
	db := newDigestTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 200 Hangul syllables: 600 bytes, so both the old byte cut and the rune
	// cut fire, and the old cut provably lands inside a character (160 is not
	// a multiple of 3).
	koreanMessage := strings.Repeat("가나다라마", 40)
	if utf8.RuneCountInString(koreanMessage) != 200 || len(koreanMessage) != 600 {
		t.Fatalf("fixture = %d runes / %d bytes, want 200/600",
			utf8.RuneCountInString(koreanMessage), len(koreanMessage))
	}
	// An ASCII message of the same rune length pins the unchanged half of the
	// contract: for single-byte text the excerpt must not move.
	asciiMessage := strings.Repeat("ab", 100)

	seedDigestFixture(t, ctx, db, koreanMessage, asciiMessage)

	sender := &capturingSender{}
	worker := NewWorker(db, sender, slog.New(slog.NewTextHandler(io.Discard, nil)), "https://moyro.example.test")

	// hour is a field precisely so tests can line it up with the clock. Retry
	// once in case the hour rolls over between reading it and tick()'s own
	// time.Now(); a mismatched hour returns before any send or stamp.
	for attempt := 0; attempt < 2 && len(sender.sent) == 0; attempt++ {
		worker.hour = time.Now().Hour()
		if err := worker.tick(ctx); err != nil {
			t.Fatalf("digest tick: %v", err)
		}
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d digests, want 1", len(sender.sent))
	}
	mail := sender.sent[0]

	t.Run("the korean excerpt reaches the recipient as valid UTF-8", func(t *testing.T) {
		if !utf8.ValidString(mail.Text) {
			t.Fatalf("text body is not valid UTF-8 (%d bytes)", len(mail.Text))
		}
		if !utf8.ValidString(mail.HTML) {
			t.Fatalf("html body is not valid UTF-8 (%d bytes)", len(mail.HTML))
		}
		if strings.ContainsRune(mail.Text, utf8.RuneError) {
			t.Fatalf("text body contains U+FFFD: %s", excerptAround(mail.Text, utf8.RuneError))
		}
		if strings.ContainsRune(mail.HTML, utf8.RuneError) {
			t.Fatalf("html body contains U+FFFD: %s", excerptAround(mail.HTML, utf8.RuneError))
		}
	})

	t.Run("the korean excerpt is cut on a character boundary", func(t *testing.T) {
		want := string([]rune(koreanMessage)[:160]) + "…"
		if !strings.Contains(mail.Text, want) {
			t.Fatalf("text body does not carry the rune-truncated excerpt\nwant substring: %q", want)
		}
	})

	t.Run("an ascii excerpt is unchanged", func(t *testing.T) {
		want := asciiMessage[:160] + "…"
		if !strings.Contains(mail.Text, want) {
			t.Fatalf("text body does not carry the ascii excerpt\nwant substring: %q", want)
		}
	})
}

// excerptAround reports a short window around the first occurrence of r so a
// failure says what the corrupted excerpt actually looked like.
func excerptAround(body string, r rune) string {
	idx := strings.IndexRune(body, r)
	if idx < 0 {
		return ""
	}
	start := idx - 40
	if start < 0 {
		start = 0
	}
	end := idx + 40
	if end > len(body) {
		end = len(body)
	}
	return strings.ToValidUTF8(body[start:end], "<invalid>")
}

func seedDigestFixture(t *testing.T, ctx context.Context, db *store.DB, koreanMessage, asciiMessage string) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, create_at, update_at)
		VALUES ('digest-reader', 'digest-reader', 'reader@example.test', '', 1, 1),
		       ('digest-author', 'digest-author', 'author@example.test', '', 1, 1)
	`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('digest-team', 'digest-team', 'Digest Team', 'O', 1, 1)
	`); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('digest-korean', 'digest-team', 'O', '공지', 'notice', 1, 1),
		       ('digest-ascii', 'digest-team', 'O', 'release', 'release', 1, 1)
	`); err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, mention_count, last_viewed_at, create_at)
		VALUES ('digest-korean', 'digest-reader', 1, 0, 1),
		       ('digest-ascii', 'digest-reader', 1, 0, 1)
	`); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO posts (id, channel_id, user_id, message, create_at, update_at)
		VALUES ('digest-post-ko', 'digest-korean', 'digest-author', $1, 2000, 2000),
		       ('digest-post-en', 'digest-ascii', 'digest-author', $2, 1000, 1000)
	`, koreanMessage, asciiMessage); err != nil {
		t.Fatalf("seed posts: %v", err)
	}
}

// newDigestTestDB hands back an isolated schema on the shared test instance,
// migrated to the current head, dropped on cleanup.
func newDigestTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(digestTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", digestTestPostgresDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open digest test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping digest test PostgreSQL: %v", err)
	}

	schemaName := "moyro_digest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create digest test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop digest test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse digest test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated digest test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated digest test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate digest test schema: %v", err)
	}
	return db
}
