# gator

`gator` is a fast, type-safe, database-backed RSS feed aggregator and reader that runs entirely within your terminal. Built with Go, PostgreSQL, and SQLC, it allows users to manage user sessions, add RSS feeds, follow other users' feeds, and continuously aggregate and browse posts seamlessly in a multi-terminal setup.

---

## Prerequisites

Before installing and configuring `gator`, ensure you have the following installed on your machine:

* **Go**: version 1.22 or higher
* **PostgreSQL**: A running instance with an active database (e.g., `gator`)

---

## Installation

Because Go programs are statically compiled, you can install the `gator` binary directly into your `$GOPATH/bin` directory using the standard toolchain:

```bash
go install [github.com/your-username/gator@latest](https://github.com/your-username/gator@latest)