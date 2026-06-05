package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"gator/internal/config"
	"gator/internal/database"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	// Side-effect import for the PostgreSQL driver
)

// state holds application configurations and database interactions.
type state struct {
	db  *database.Queries
	cfg *config.Config
}

type command struct {
	name string
	args []string
}

type commands struct {
	registeredCommands map[string]func(*state, command) error
}

func (c *commands) register(name string, f func(*state, command) error) {
	c.registeredCommands[name] = f
}

func (c *commands) run(s *state, cmd command) error {
	handler, exists := c.registeredCommands[cmd.name]
	if !exists {
		return fmt.Errorf("command '%s' not found", cmd.name)
	}
	return handler(s, cmd)
}

func main() {
	// 1. Read configuration
	cfg, err := config.Read()
	if err != nil {
		log.Fatalf("error reading config: %v", err)
	}

	// 2. Establish connection to PostgreSQL database
	db, err := sql.Open("postgres", cfg.DBUrl)
	if err != nil {
		log.Fatalf("error connecting to database: %v", err)
	}
	defer db.Close()

	// 3. Initialize SQLC queries and application state
	dbQueries := database.New(db)
	appState := &state{
		db:  dbQueries,
		cfg: &cfg,
	}

	// 4. Register commands
	cmds := commands{
		registeredCommands: make(map[string]func(*state, command) error),
	}
	cmds.register("login", handlerLogin)
	cmds.register("register", handlerRegister)
	cmds.register("reset", handlerReset)
	cmds.register("users", handlerUsers)
	cmds.register("agg", handlerAgg)
	cmds.register("feeds", handlerFeeds)
	cmds.register("addfeed", middlewareLoggedIn(handlerAddFeed))
	cmds.register("follow", middlewareLoggedIn(handlerFollow))
	cmds.register("following", middlewareLoggedIn(handlerFollowing))
	cmds.register("unfollow", middlewareLoggedIn(handlerUnfollow))
	cmds.register("browse", middlewareLoggedIn(handlerBrowse))

	// 5. Run incoming CLI command
	if len(os.Args) < 2 {
		log.Fatal("error: not enough arguments provided.")
	}

	cmdName := os.Args[1]
	cmdArgs := os.Args[2:]

	userCmd := command{
		name: cmdName,
		args: cmdArgs,
	}

	err = cmds.run(appState, userCmd)
	if err != nil {
		log.Fatalf("command error: %v", err)
	}
}

// handlerRegister creates a new unique user in the database.
func handlerRegister(s *state, cmd command) error {
	if len(cmd.args) == 0 {
		return errors.New("the register command expects a username argument")
	}

	username := cmd.args[0]

	// Request context to complete database operation
	ctx := context.Background()

	// Use generated SQLC method to add a user
	user, err := s.db.CreateUser(ctx, database.CreateUserParams{
		ID:        uuid.New(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Name:      username,
	})
	if err != nil {
		// Exit with status code 1 if user already exists
		return fmt.Errorf("could not register user (it may already exist): %w", err)
	}

	// Automatically log in the newly registered user
	err = s.cfg.SetUser(username)
	if err != nil {
		return fmt.Errorf("user registered but configuration update failed: %w", err)
	}

	fmt.Printf("User '%s' was successfully created!\n", username)
	fmt.Printf("Debug Info: %+v\n", user)
	return nil
}

// handlerLogin logs in an existing user, or returns an error if they don't exist.
func handlerLogin(s *state, cmd command) error {
	if len(cmd.args) == 0 {
		return errors.New("the login command expects a username argument")
	}

	username := cmd.args[0]
	ctx := context.Background()

	// Check if the user exists in the database
	_, err := s.db.GetUser(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("cannot login: user '%s' does not exist", username)
		}
		return fmt.Errorf("database lookup failed: %w", err)
	}

	// Update configuration file
	err = s.cfg.SetUser(username)
	if err != nil {
		return fmt.Errorf("could not update login configuration: %w", err)
	}

	fmt.Printf("User has been successfully logged in as: %s\n", username)
	return nil
}

// handlerReset deletes all users from the database to reset the application state.
func handlerReset(s *state, cmd command) error {
	ctx := context.Background()

	// Call the SQLC generated method to purge the table
	err := s.db.DeleteUsers(ctx)
	if err != nil {
		return fmt.Errorf("failed to reset database: %w", err)
	}

	fmt.Println("Database successfully reset! All users have been cleared.")
	return nil
}

// handlerUsers retrieves all users from the database and prints them,
// highlighting the currently logged-in user.
func handlerUsers(s *state, cmd command) error {
	ctx := context.Background()

	// 1. Fetch all users from the database using the SQLC generated method
	users, err := s.db.GetUsers(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve users: %w", err)
	}

	// 2. Loop through the slice and print each username
	for _, user := range users {
		if user.Name == s.cfg.CurrentUserName {
			// If the user matches the one in our config file, append (current)
			fmt.Printf("* %s (current)\n", user.Name)
		} else {
			fmt.Printf("* %s\n", user.Name)
		}
	}

	return nil
}

// handlerAgg fetches a target RSS feed and prints its structure out for testing purposes.
// handlerAgg initializes a recurring time ticker to indefinitely scrape feeds.
func handlerAgg(s *state, cmd command) error {
	if len(cmd.args) < 1 {
		return errors.New("the agg command expects a duration argument string (e.g., '1s', '1m', '1h')")
	}

	// 1. Attempt to parse the duration metrics format
	timeBetweenRequests, err := time.ParseDuration(cmd.args[0])
	if err != nil {
		return fmt.Errorf("invalid duration parameter configuration: %w", err)
	}

	fmt.Printf("Collecting feeds every %s\n", timeBetweenRequests)

	// 2. Establish a background time interval controller loop
	ticker := time.NewTicker(timeBetweenRequests)

	// Pre-executes immediately, then executes on every subsequent clock cycle drop
	for ; ; <-ticker.C {
		scrapeFeeds(s)
	}
}

// handlerAddFeed creates a new feed record associated with the current user.
// func handlerAddFeed(s *state, cmd command) error {
// 	// 1. Verify we received exactly two arguments: name and url
// 	if len(cmd.args) < 2 {
// 		return errors.New("the addfeed command expects two arguments: a name and a url")
// 	}

// 	feedName := cmd.args[0]
// 	feedURL := cmd.args[1]
// 	ctx := context.Background()

// 	// 2. Fetch the current logged-in user from the database to get their UUID
// 	user, err := s.db.GetUser(ctx, s.cfg.CurrentUserName)
// 	if err != nil {
// 		if errors.Is(err, sql.ErrNoRows) {
// 			return fmt.Errorf("current user '%s' does not exist in the database", s.cfg.CurrentUserName)
// 		}
// 		return fmt.Errorf("failed to fetch current user: %w", err)
// 	}

// 	// 3. Save the new feed into the database
// 	feed, err := s.db.CreateFeed(ctx, database.CreateFeedParams{
// 		ID:        uuid.New(),
// 		CreatedAt: time.Now().UTC(),
// 		UpdatedAt: time.Now().UTC(),
// 		Name:      feedName,
// 		Url:       feedURL,
// 		UserID:    user.ID,
// 	})
// 	if err != nil {
// 		return fmt.Errorf("could not create feed: %w", err)
// 	}

// 	// 4. Print out the resulting record fields
// 	fmt.Println("Feed successfully added!")
// 	fmt.Printf("* ID:         %s\n", feed.ID)
// 	fmt.Printf("* Name:       %s\n", feed.Name)
// 	fmt.Printf("* URL:        %s\n", feed.Url)
// 	fmt.Printf("* Created At: %s\n", feed.CreatedAt)
// 	fmt.Printf("* Updated At: %s\n", feed.UpdatedAt)
// 	fmt.Printf("* User ID:    %s\n", feed.UserID)

// 	return nil
// }

// handlerFeeds retrieves all feeds from the database alongside their creator's name.
func handlerFeeds(s *state, cmd command) error {
	ctx := context.Background()

	// 1. Fetch all feeds via the new JOIN query
	feeds, err := s.db.GetFeeds(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve feeds: %w", err)
	}

	// 2. Return early if no feeds exist yet
	if len(feeds) == 0 {
		fmt.Println("No feeds found in the database.")
		return nil
	}

	// 3. Iterate and print out the properties
	for _, feed := range feeds {
		fmt.Printf("* Name:      %s\n", feed.Name)
		fmt.Printf("  URL:       %s\n", feed.Url)
		fmt.Printf("  CreatedBy: %s\n", feed.UserName)
		fmt.Println("  ---")
	}

	return nil
}

// handlerAddFeed creates a new feed and automatically logs a follow record for the creator.
func handlerAddFeed(s *state, cmd command, user database.User) error {
	if len(cmd.args) < 2 {
		return errors.New("the addfeed command expects two arguments: a name and a url")
	}

	feedName := cmd.args[0]
	feedURL := cmd.args[1]
	ctx := context.Background()

	// Look up code REMOVED. We use 'user.ID' directly now!
	feed, err := s.db.CreateFeed(ctx, database.CreateFeedParams{
		ID:        uuid.New(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Name:      feedName,
		Url:       feedURL,
		UserID:    user.ID,
	})
	if err != nil {
		return fmt.Errorf("could not create feed: %w", err)
	}

	_, err = s.db.CreateFeedFollow(ctx, database.CreateFeedFollowParams{
		ID:        uuid.New(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		UserID:    user.ID,
		FeedID:    feed.ID,
	})
	if err != nil {
		return fmt.Errorf("feed created, but auto-follow registration failed: %w", err)
	}

	fmt.Println("Feed successfully added and followed!")
	fmt.Printf("* Name: %s\n* Url:  %s\n", feed.Name, feed.Url)
	return nil
}

// handlerFollow creates a brand new feed follow link for the active user session.
func handlerFollow(s *state, cmd command, user database.User) error {
	if len(cmd.args) == 0 {
		return errors.New("the follow command expects a target feed url argument")
	}

	feedURL := cmd.args[0]
	ctx := context.Background()

	// Look up code REMOVED. We use 'user.ID' directly now!
	feed, err := s.db.GetFeedByUrl(ctx, feedURL)
	if err != nil {
		return fmt.Errorf("could not find a matching registered feed for that URL: %w", err)
	}

	followRecord, err := s.db.CreateFeedFollow(ctx, database.CreateFeedFollowParams{
		ID:        uuid.New(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		UserID:    user.ID,
		FeedID:    feed.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to process feed follow interaction: %w", err)
	}

	fmt.Printf("Success! User '%s' is now following feed: '%s'\n", followRecord.UserName, followRecord.FeedName)
	return nil
}

// handlerFollowing prints out a comprehensive list of all feeds followed by the current user.
func handlerFollowing(s *state, cmd command, user database.User) error {
	ctx := context.Background()

	// Look up code REMOVED. We use 'user.ID' directly now!
	follows, err := s.db.GetFeedFollowsForUser(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("failed to retrieve subscription feeds: %w", err)
	}

	if len(follows) == 0 {
		fmt.Printf("User '%s' isn't following any feeds yet.\n", user.Name)
		return nil
	}

	fmt.Printf("Feeds currently followed by %s:\n", user.Name)
	for _, follow := range follows {
		fmt.Printf("* %s\n", follow.FeedName)
	}

	return nil
}

// handlerUnfollow removes a feed follow record for the current logged-in user.
func handlerUnfollow(s *state, cmd command, user database.User) error {
	// 1. Verify a feed URL argument was passed
	if len(cmd.args) == 0 {
		return errors.New("the unfollow command expects a target feed url argument")
	}

	feedURL := cmd.args[0]
	ctx := context.Background()

	// 2. Find the feed by its URL to get its feed_id
	feed, err := s.db.GetFeedByUrl(ctx, feedURL)
	if err != nil {
		return fmt.Errorf("could not find a matching registered feed for that URL: %w", err)
	}

	// 3. Execute the deletion query using the user ID and feed ID
	err = s.db.DeleteFeedFollow(ctx, database.DeleteFeedFollowParams{
		UserID: user.ID,
		FeedID: feed.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to unfollow feed: %w", err)
	}

	fmt.Printf("Success! You have unfollowed feed: '%s'\n", feed.Name)
	return nil
}

// handlerBrowse displays recently aggregated posts from feeds the current user follows.
func handlerBrowse(s *state, cmd command, user database.User) error {
	limit := 2 // default limit value

	// Check if a custom parameter threshold override was passed
	if len(cmd.args) > 0 {
		parsedLimit, err := strconv.Atoi(cmd.args[0])
		if err != nil {
			return fmt.Errorf("invalid limit parameter (must be an integer): %w", err)
		}
		if parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	ctx := context.Background()
	posts, err := s.db.GetPostsForUser(ctx, database.GetPostsForUserParams{
		UserID: user.ID,
		Limit:  int32(limit),
	})
	if err != nil {
		return fmt.Errorf("failed to load browse view items: %w", err)
	}

	if len(posts) == 0 {
		fmt.Println("No posts found. Make sure you are following feeds and that the 'agg' command is running!")
		return nil
	}

	fmt.Printf("--- Browsing Latest %d Posts ---\n", len(posts))
	for _, post := range posts {
		pubTime := "Unknown"
		if post.PublishedAt.Valid {
			pubTime = post.PublishedAt.Time.Local().Format("2006-01-02 15:04")
		}

		fmt.Printf("\n📢  %s\n", post.Title)
		fmt.Printf("   Published: %s\n", pubTime)
		fmt.Printf("   Link:      %s\n", post.Url)
		if post.Description.Valid && post.Description.String != "" {
			// Truncate long body descriptions slightly for cleaner terminal display
			desc := post.Description.String
			if len(desc) > 180 {
				desc = desc[:177] + "..."
			}
			fmt.Printf("   Summary:   %s\n", desc)
		}
		fmt.Println("   " + strings.Repeat("-", 40))
	}

	return nil
}

// middlewareLoggedIn wraps a specialized command handler that requires an authenticated database user.
// It intercepts the call, performs the user validation, and passes the user record down.
func middlewareLoggedIn(handler func(s *state, cmd command, user database.User) error) func(*state, command) error {
	return func(s *state, cmd command) error {
		ctx := context.Background()

		// 1. Centralized user authentication lookup
		user, err := s.db.GetUser(ctx, s.cfg.CurrentUserName)
		if err != nil {
			return fmt.Errorf("authentication required: user '%s' does not exist: %w", s.cfg.CurrentUserName, err)
		}

		// 2. Forward the execution to our actual inner handler
		return handler(s, cmd, user)
	}
}
