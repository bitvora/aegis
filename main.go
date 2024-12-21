package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/fiatjaf/eventstore/lmdb"
	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/khatru/blossom"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3" // SQLite driver
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/spf13/afero"
)

type Whitelist struct {
	Pubkeys []string `json:"pubkeys"`
}

var sqlDB *sql.DB
var err error
var fs afero.Fs

func loadWhitelist() (*Whitelist, error) {
	query := `SELECT pubkey FROM subscriptions WHERE active = true;`
	rows, err := sqlDB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query subscriptions: %w", err)
	}
	defer rows.Close()

	var pubkeys []string
	for rows.Next() {
		var pubkey string
		if err := rows.Scan(&pubkey); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		pubkeys = append(pubkeys, pubkey)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over rows: %w", err)
	}

	return &Whitelist{Pubkeys: pubkeys}, nil
}

func nPubToPubkey(nPub string) string {
	_, v, err := nip19.Decode(nPub)
	if err != nil {
		log.Println("failed to decode npub:", err)
		return ""
	}
	return v.(string)
}

func main() {
	godotenv.Load(".env")

	var art = `
 █████╗ ███████╗ ██████╗ ██╗███████╗
██╔══██╗██╔════╝██╔════╝ ██║██╔════╝
███████║█████╗  ██║  ███╗██║███████╗
██╔══██║██╔══╝  ██║   ██║██║╚════██║
██║  ██║███████╗╚██████╔╝██║███████║
╚═╝  ╚═╝╚══════╝ ╚═════╝ ╚═╝╚══════╝
PREMIUM RELAY & BLOSSOM SERVER
	`

	nostr.InfoLogger = log.New(io.Discard, "", 0)
	green := "\033[32m"
	reset := "\033[0m"
	fmt.Println(green + art + reset)
	log.Println("🚀 Aegis is booting up")

	relay := khatru.NewRelay()

	relay.Info.Name = os.Getenv("RELAY_NAME")
	relay.Info.PubKey = os.Getenv("RELAY_PUBKEY")
	relay.Info.Icon = os.Getenv("RELAY_ICON")
	relay.Info.Contact = os.Getenv("RELAY_CONTACT")
	relay.Info.Description = os.Getenv("RELAY_DESCRIPTION")
	relay.Info.Software = "https://github.com/bitvora/sw2"
	relay.Info.Version = "0.1.0"
	blossomPath := os.Getenv("BLOSSOM_PATH")
	relayUrl := os.Getenv("RELAY_URL")
	dbPath := os.Getenv("DB_PATH")
	relayPort := os.Getenv("RELAY_PORT")
	relay.ServiceURL = "wss://" + relayUrl

	sqlDB, err = sql.Open("sqlite3", dbPath+"subscriptions.db")
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer sqlDB.Close()

	if err := migrateDB(sqlDB); err != nil {
		log.Fatalf("failed to run database migrations: %v", err)
	}

	db := lmdb.LMDBBackend{
		Path: dbPath,
	}

	fs = afero.NewOsFs()
	fs.MkdirAll(blossomPath, 0755)

	if err := db.Init(); err != nil {
		panic(err)
	}

	whitelist, err := loadWhitelist()
	if err != nil {
		fmt.Println("Error loading config:", err)
		return
	}

	for _, pubkey := range whitelist.Pubkeys {
		fmt.Println(pubkey)
	}

	relay.RejectEvent = append(relay.RejectEvent, func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
		if event.PubKey == "" {
			return true, "no pubkey"
		}

		for _, pubkey := range whitelist.Pubkeys {
			if pubkey == event.PubKey {
				return false, ""
			}
		}

		return true, "pubkey not whitelisted"
	})

	relay.StoreEvent = append(relay.StoreEvent, db.SaveEvent)
	relay.QueryEvents = append(relay.QueryEvents, db.QueryEvents)
	relay.CountEvents = append(relay.CountEvents, db.CountEvents)
	relay.DeleteEvent = append(relay.DeleteEvent, db.DeleteEvent)

	go checkExpiredSubscriptions()

	mux := relay.Router()
	mux.HandleFunc("/bitvora_webhook", handleBitvoraWebhook)
	mux.HandleFunc("/generate_invoice", handleGenerateInvoice)
	mux.HandleFunc("/poll_payment", handlePollPayment)
	mux.HandleFunc("/", handleHomePage)

	bl := blossom.New(relay, "https://"+relayUrl)
	bl.ServiceURL = relay.ServiceURL
	bl.Store = blossom.EventStoreBlobIndexWrapper{Store: &db, ServiceURL: bl.ServiceURL}
	bl.StoreBlob = append(bl.StoreBlob, func(ctx context.Context, sha256 string, body []byte) error {

		file, err := fs.Create(blossomPath + sha256)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, bytes.NewReader(body)); err != nil {
			return err
		}
		return nil
	})
	bl.LoadBlob = append(bl.LoadBlob, func(ctx context.Context, sha256 string) (io.ReadSeeker, error) {
		return fs.Open(blossomPath + sha256)
	})
	bl.DeleteBlob = append(bl.DeleteBlob, func(ctx context.Context, sha256 string) error {
		return fs.Remove(blossomPath + sha256)
	})
	bl.RejectUpload = append(bl.RejectUpload, func(ctx context.Context, event *nostr.Event, size int, ext string) (bool, string, int) {
		for _, pubkey := range whitelist.Pubkeys {
			if pubkey == event.PubKey {
				return false, ext, size
			}
		}

		return true, "you must have an active subscription to upload to this server", 403
	})

	log.Println("🚀 Server started on port", relayPort)
	http.ListenAndServe("0.0.0.0:"+relayPort, relay)

}

func checkExpiredSubscriptions() {
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			query := `
			UPDATE subscriptions
			SET active = false
			WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP;
			`
			_, err := sqlDB.Exec(query)
			if err != nil {
				log.Printf("failed to update expired subscriptions: %v", err)
			} else {
				log.Println("Checked and updated expired subscriptions")
			}
		}
	}()
}
