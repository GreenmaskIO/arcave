// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command arcave-demo-app is a tiny web app for the arcave demo. It reads and
// writes three stores, each backed by a different Docker volume type, so a
// `arcave savepoint create` / `arcave rollback` cycle can be observed end to end:
//
//	PostgreSQL -> named volume
//	MinIO      -> named volume
//	Redis      -> anonymous volume
//
// The UI at http://localhost:8080 lists the current entries of each store and
// lets you append new ones. After a rollback, refreshing the page shows every
// store restored to its savepoint state.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
)

//go:embed index.html
var indexHTML string

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

// redisListKey is the Redis list the demo appends notes to.
const redisListKey = "notes"

type app struct {
	pg     *pgxpool.Pool
	rdb    *redis.Client
	mc     *minio.Client
	bucket string
}

func main() {
	ctx := context.Background()

	a, err := newApp(ctx)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	if err := a.bootstrap(ctx); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/add/postgres", a.handleAddPostgres)
	mux.HandleFunc("/add/redis", a.handleAddRedis)
	mux.HandleFunc("/add/minio", a.handleAddMinio)
	mux.HandleFunc("/healthz", a.handleHealthz)

	log.Println("demo app listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}

// newApp connects to every store, retrying until each is reachable or the
// overall deadline elapses. depends_on healthchecks usually mean the stores are
// already up, but retrying keeps the app robust to a slow first start.
func newApp(ctx context.Context) (*app, error) {
	pgDSN := env("PG_DSN", "postgres://postgres:demo@postgres:5432/demo?sslmode=disable")
	redisAddr := env("REDIS_ADDR", "redis:6379")
	minioEndpoint := env("MINIO_ENDPOINT", "minio:9000")
	minioAccess := env("MINIO_ACCESS_KEY", "minioadmin")
	minioSecret := env("MINIO_SECRET_KEY", "minioadmin")
	bucket := env("MINIO_BUCKET", "demo")

	deadline := time.Now().Add(60 * time.Second)

	var pg *pgxpool.Pool
	for {
		var err error
		pg, err = pgxpool.New(ctx, pgDSN)
		if err == nil {
			err = pg.Ping(ctx)
		}
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		time.Sleep(time.Second)
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	for {
		if err := rdb.Ping(ctx).Err(); err == nil {
			break
		} else if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect redis: %w", err)
		}
		time.Sleep(time.Second)
	}

	mc, err := minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccess, minioSecret, ""),
		Secure: false,
	})
	if err != nil {
		return nil, fmt.Errorf("connect minio: %w", err)
	}

	return &app{pg: pg, rdb: rdb, mc: mc, bucket: bucket}, nil
}

// bootstrap creates the Postgres table and the MinIO bucket, then seeds each
// store with distinct, recognizable entries if it is currently empty so the
// "different data per store" story is visible immediately.
func (a *app) bootstrap(ctx context.Context) error {
	if _, err := a.pg.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS notes (
			id         SERIAL PRIMARY KEY,
			text       TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create notes table: %w", err)
	}

	exists, err := a.mc.BucketExists(ctx, a.bucket)
	if err != nil {
		return fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := a.mc.MakeBucket(ctx, a.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("create bucket: %w", err)
		}
	}

	// Seed Postgres.
	var pgCount int
	if err := a.pg.QueryRow(ctx, `SELECT count(*) FROM notes`).Scan(&pgCount); err != nil {
		return fmt.Errorf("count notes: %w", err)
	}
	if pgCount == 0 {
		for i := 1; i <= 2; i++ {
			if _, err := a.pg.Exec(ctx,
				`INSERT INTO notes (text) VALUES ($1)`,
				fmt.Sprintf("Postgres row #%d (named volume)", i)); err != nil {
				return fmt.Errorf("seed postgres: %w", err)
			}
		}
	}

	// Seed Redis.
	n, err := a.rdb.LLen(ctx, redisListKey).Result()
	if err != nil {
		return fmt.Errorf("redis llen: %w", err)
	}
	if n == 0 {
		for _, s := range []string{"Redis item A (anonymous volume)", "Redis item B (anonymous volume)"} {
			if err := a.rdb.RPush(ctx, redisListKey, s).Err(); err != nil {
				return fmt.Errorf("seed redis: %w", err)
			}
		}
	}

	// Seed MinIO.
	objs, err := a.listMinio(ctx)
	if err != nil {
		return fmt.Errorf("list minio: %w", err)
	}
	if len(objs) == 0 {
		body := "MinIO object (named volume)"
		if err := a.putMinio(ctx, "seed-1.txt", body); err != nil {
			return fmt.Errorf("seed minio: %w", err)
		}
	}

	return nil
}

// pageData is the view model rendered by index.html.
type pageData struct {
	Postgres []string
	Redis    []string
	Minio    []string
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	data := pageData{}

	rows, err := a.pg.Query(ctx, `SELECT text FROM notes ORDER BY id`)
	if err != nil {
		httpError(w, err)
		return
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			httpError(w, err)
			return
		}
		data.Postgres = append(data.Postgres, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpError(w, err)
		return
	}

	if data.Redis, err = a.rdb.LRange(ctx, redisListKey, 0, -1).Result(); err != nil {
		httpError(w, err)
		return
	}

	if data.Minio, err = a.listMinio(ctx); err != nil {
		httpError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, data); err != nil {
		log.Printf("render: %v", err)
	}
}

func (a *app) handleAddPostgres(w http.ResponseWriter, r *http.Request) {
	text := formText(w, r)
	if text == "" {
		return
	}
	if _, err := a.pg.Exec(r.Context(), `INSERT INTO notes (text) VALUES ($1)`, text); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) handleAddRedis(w http.ResponseWriter, r *http.Request) {
	text := formText(w, r)
	if text == "" {
		return
	}
	if err := a.rdb.RPush(r.Context(), redisListKey, text).Err(); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) handleAddMinio(w http.ResponseWriter, r *http.Request) {
	text := formText(w, r)
	if text == "" {
		return
	}
	key := fmt.Sprintf("note-%d.txt", time.Now().UnixNano())
	if err := a.putMinio(r.Context(), key, text); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleHealthz reports 200 only when all three stores are reachable; the
// compose healthcheck uses it so `up --wait` blocks until the app is wired up.
func (a *app) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := a.pg.Ping(ctx); err != nil {
		http.Error(w, "postgres: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := a.rdb.Ping(ctx).Err(); err != nil {
		http.Error(w, "redis: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if _, err := a.mc.BucketExists(ctx, a.bucket); err != nil {
		http.Error(w, "minio: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

// listMinio returns every object's "name: contents" rendered for display,
// sorted by key so the listing is stable.
func (a *app) listMinio(ctx context.Context) ([]string, error) {
	var keys []string
	for obj := range a.mc.ListObjects(ctx, a.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		keys = append(keys, obj.Key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		body, err := a.getMinio(ctx, key)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s — %s", key, body))
	}
	return out, nil
}

func (a *app) putMinio(ctx context.Context, key, body string) error {
	_, err := a.mc.PutObject(ctx, a.bucket, key, strings.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: "text/plain"})
	return err
}

func (a *app) getMinio(ctx context.Context, key string) (string, error) {
	obj, err := a.mc.GetObject(ctx, a.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return "", err
	}
	defer obj.Close()
	body, err := io.ReadAll(obj)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func formText(w http.ResponseWriter, r *http.Request) string {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return ""
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, err)
		return ""
	}
	text := r.FormValue("text")
	if text == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
	return text
}

func httpError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
