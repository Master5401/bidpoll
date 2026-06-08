package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"

	migrate "github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	godotenv "github.com/joho/godotenv"
	_ "github.com/lib/pq"

	discordAdapter "github.com/shakunth/bidpoll/internal/adapters/discord"
	eventbusAdapter "github.com/shakunth/bidpoll/internal/adapters/eventbus"
	postgresAdapter "github.com/shakunth/bidpoll/internal/adapters/postgres"
	"github.com/shakunth/bidpoll/internal/core/application"
	"github.com/shakunth/bidpoll/internal/core/domain"
)

func main() {
	// 1. Connect to the physical Postgres database
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found. Falling back to system environment variables.")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is not set. The vault is locked.")
	}

	// 1. Connect to the physical Postgres database using the secret URL
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	defer db.Close()

	// 2. Automate the Infrastructure
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		log.Fatalf("Could not create migration driver: %v", err)
	}

	m, err := migrate.NewWithDatabaseInstance("file://migrations", "postgres", driver)
	if err != nil {
		log.Fatalf("Could not initialize migrations: %v", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("Migration failed: %v", err)
	}
	log.Println("[BOOT] Database schemas verified and loaded.")

	// 3. Wire the Hexagonal Architecture
	bus := eventbusAdapter.NewInMemoryEventBus()
	repo := postgresAdapter.NewPollRepo(db)
	engine := application.NewPollEngine(repo, bus)

	// 4. Set up an observer to prove the Event Bus works
	bus.Subscribe(domain.EvtOptionClaimed, func(ctx context.Context, evt domain.PollEvent) error {
		log.Printf("[EVENT LOG] Broadcast received! User %s claimed Option %s", evt.UserID, evt.OptionID)
		return nil
	})

	// 5. Wire the Inbound Adapter (The Front Gate)
	discordPubKey := os.Getenv("DISCORD_PUBLIC_KEY")
	discordHandler := discordAdapter.NewHandler(engine, discordPubKey)

	// Route incoming Discord traffic strictly through the handler
	http.HandleFunc("/api/interactions", discordHandler.HandleInteraction)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Fallback for local development
	}

	log.Printf("[BOOT] BidPoll Engine online. Listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
