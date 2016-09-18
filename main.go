package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/facebookgo/flagenv"
	"github.com/gorilla/mux"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func main() {
	var port int
	flag.IntVar(&port, "port", 9126, "port to run site")
	flagenv.Parse()
	flag.Parse()

	r := mux.NewRouter()
	r.HandleFunc("/", indexHandler)
	r.HandleFunc("/unauth", somethingHandler)
	r.Handle("/auth", mwAuth(http.HandlerFunc(anotherHandler)))

	http.Handle("/", mwPanic(mwLog(r)))

	log.Printf("starting on :%d", port)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
		log.Println("Unexpected error serving: ", err.Error())
	}
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("index handler")
}

func somethingHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("handle unauth")
}

func anotherHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("handle auth")
}

func mwAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Println("requires auth", r.URL)
		h.ServeHTTP(w, r)
	})
}

func mwPanic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logEvent(r, "panic", fmt.Sprintf("%v %s", rec, debug.Stack()))
			}
		}()
		h.ServeHTTP(w, r)
	})
}

func logDataGet(r *http.Request) map[string]interface{} {
	ctx := r.Context()
	data := ctx.Value("log")
	switch v := data.(type) {
	case map[string]interface{}:
		return v
	}
	return make(map[string]interface{})
}

func logDataAdd(r *http.Request, key string, value interface{}) {
	var data map[string]interface{}

	ctx := r.Context()
	d := ctx.Value("log")
	switch v := d.(type) {
	case map[string]interface{}:
		data = v
	default:
		data = make(map[string]interface{})
	}

	data[key] = value

	r = r.WithContext(context.WithValue(ctx, "log", data))
}

func logDataReplace(r *http.Request, data map[string]interface{}) {
	ctx := r.Context()
	r = r.WithContext(context.WithValue(ctx, "log", data))
}

var ranOnce bool

func mwLog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logData := logDataGet(r)
		logData["request_time"] = start.Unix()
		logData["request_id"] = fmt.Sprintf("%08x", rand.Int63n(1e9))
		logData["event"] = "request"
		logData["remote_addr"] = r.RemoteAddr
		logData["method"] = r.Method
		logData["url"] = r.URL.String()
		logData["content_length"] = r.ContentLength

		// init the logger's response writer used to caputure the status code
		// pull from a pool, set the writer, initialize / reset the response code to a sensible default, reset that this response writer has been used
		// for the logging middleware (based on noodle's logger middleware)
		// could put the ranOnce in the init, but I want to make copy-pasta easier if I use mwLog again (before turning it into a real package)
		if !ranOnce {
			ranOnce = true
			writers.New = func() interface{} {
				return &logWriter{}
			}
		}
		lw := writers.Get().(*logWriter)
		lw.ResponseWriter = w
		lw.code = http.StatusOK
		lw.headerWritten = false
		defer writers.Put(lw)

		h.ServeHTTP(lw, r)

		logData["code"] = lw.Code()
		logData["tts_ns"] = time.Since(start).Nanoseconds() / 1e6 // time to serve in nano seconds

		log.Println(logAsString(logData))
	})
}

func logAsString(l map[string]interface{}) string {
	b, err := json.Marshal(l)
	if err != nil {
		logError(nil, err, "unable to marshal map[string]interface{}")
	}
	return string(b)
}

// logEvent allows us to track novel happeningsf
func logEvent(r *http.Request, event string, msg string) {
	logData := logDataGet(r)
	logData["event"] = event
	logData["message"] = msg

	log.Println(logAsString(logData))
}

// logError is similar to logEvent but has an error field
func logError(r *http.Request, err error, msg string) {
	logData := logDataGet(r)
	logData["event"] = "error"
	logData["message"] = msg
	if err == nil {
		err = fmt.Errorf("internal error condition")
	}
	logData["error"] = err.Error()

	log.Println(logAsString(logData))
}

// everything below is for the logger mw (from noodle)

// logWriter mimics http.ResponseWriter functionality while storing
// HTTP status code for later logging
type logWriter struct {
	code          int
	headerWritten bool
	http.ResponseWriter
}

func (l *logWriter) WriteHeader(code int) {
	l.headerWritten = false
	if !l.headerWritten {
		l.ResponseWriter.WriteHeader(code)
		l.code = code
		l.headerWritten = true
	}
}

func (l *logWriter) Write(buf []byte) (int, error) {
	l.headerWritten = true
	return l.ResponseWriter.Write(buf)
}

func (l *logWriter) Code() int {
	return l.code
}

// provide other typical ResponseWriter methods
func (l *logWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return l.ResponseWriter.(http.Hijacker).Hijack()
}

func (l *logWriter) CloseNotify() <-chan bool {
	return l.ResponseWriter.(http.CloseNotifier).CloseNotify()
}

func (l *logWriter) Flush() {
	l.ResponseWriter.(http.Flusher).Flush()
}

var writers sync.Pool
