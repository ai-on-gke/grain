// Copyright 2026 Google LLC
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

//go:build ignore

// pgcheck opens a DSN with the pgx stdlib driver grain's daemon uses
// (pkg/model/postgres) and reports whether the session is TLS.
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	db, err := sql.Open("pgx", os.Args[1])
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	var user string
	var ssl bool
	if err := db.QueryRow("SELECT current_user, ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").Scan(&user, &ssl); err != nil {
		fmt.Println("refused:", err)
		os.Exit(1)
	}
	fmt.Printf("connected as %s, ssl=%v\n", user, ssl)
}
