// ***************************************************************************
//
//  Copyright 2017 David (Dizzy) Smith, dizzyd@dizzyd.com
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.
// ***************************************************************************

package main

import (
	"compress/bzip2"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"regexp"

	_ "github.com/mattn/go-sqlite3"
)

type Database struct {
	sqlDb     *sql.DB
	sqlDbPath string
}

func OpenDatabase() (*Database, error) {
	db := new(Database)

	db.sqlDbPath = filepath.Join(env().McdexDir, "mcdex.dat")
	if !fileExists(db.sqlDbPath) {
		return nil, fmt.Errorf("No database available; use db.update command first")
	}

	sqlDb, err := sql.Open("sqlite3", db.sqlDbPath)
	if err != nil {
		return nil, err
	}

	db.sqlDb = sqlDb

	return db, nil
}

func InstallDatabase() error {
	// Get the latest version
	version, err := getLatestVersion("data")
	if err != nil {
		return err
	}

	// Download the latest data file to mcdex/mcdex.dat
	url := fmt.Sprintf("http://files.mcdex.net/data/mcdex-%s.dat.bz2", version)
	res, err := HttpGet(url)
	if err != nil {
		return fmt.Errorf("Failed to retrieve %s data file: %+v", version, err)
	}
	defer res.Body.Close()

	// Stream the data file to mcdex.dat.tmp
	tmpFileName := filepath.Join(env().McdexDir, "mcdex.dat.tmp")
	err = writeStream(tmpFileName, bzip2.NewReader(res.Body))
	if err != nil {
		return err
	}

	// Open the temporary database and validate it
	tmpDb, err := sql.Open("sqlite3", tmpFileName)
	if err != nil {
		// TODO: Add log entry about the file being corrupt
		return nil
	}
	defer tmpDb.Close()

	_, err = tmpDb.Exec("PRAGMA integrity_check;")
	if err != nil {
		return nil
	}

	// Force the tmpDb to close so that (on Windows), we can ensure
	// the rename works
	tmpDb.Close()

	// Close the database and rename the tmp file
	err = os.Rename(tmpFileName, filepath.Join(env().McdexDir, "mcdex.dat"))
	if err != nil {
		return fmt.Errorf("Failed to rename mcdex.dat.tmp: %+v", err)
	}

	fmt.Printf("Updated mod database.\n")

	return nil
}

func (db *Database) listForge(mcvsn string, verbose bool) error {
	rows, err := db.sqlDb.Query("select version, isrec from forge where mcvsn = ? order by version desc", mcvsn)
	switch {
	case err == sql.ErrNoRows:
		return fmt.Errorf("No Forge version found for %s", mcvsn)
	case err != nil:
		return err
	}

	latest := false

	defer rows.Close()
	for rows.Next() {
		var version string
		var isrec bool
		err := rows.Scan(&version, &isrec)
		if err != nil {
			return err
		}
		if isrec {
			fmt.Printf("%s (recommended)\n", version)
		} else if !latest {
			fmt.Printf("%s (latest)\n", version)
			latest = true
		} else if verbose {
			fmt.Printf("%s\n", version)
		}
	}
	return nil
}

func (db *Database) lookupForgeVsn(mcvsn string) (string, error) {
	var forgeVsn string
	err := db.sqlDb.QueryRow("select version from forge where mcvsn = ? and isrec = 1", mcvsn).Scan(&forgeVsn)
	switch {
	case err == sql.ErrNoRows:
		return "", fmt.Errorf("No Forge version found for %s", mcvsn)
	case err != nil:
		return "", err
	}
	return forgeVsn, nil
}

func (db *Database) listMods(name, mcvsn string) error {
	// Turn the name into a pre-compiled regex
	nameRegex, err := regexp.Compile(name)
	if err != nil {
		return fmt.Errorf("Failed to convert %s into regex: %s", name, err)
	}

	query := "select name, description from mods where rowid in (select modid from filevsns where version = ?) order by name"
	if mcvsn == "" {
		query = "select name, description from mods order by name"
	}

	rows, err := db.sqlDb.Query(query, mcvsn)
	if err != nil {
		return fmt.Errorf("Query failed: %+v", err)
	}
	defer rows.Close()

	// For each row, check the name against the pre-compiled regex
	for rows.Next() {
		var modName, modDesc string
		err = rows.Scan(&modName, &modDesc)
		if err != nil {
			return err
		}

		if nameRegex.MatchString(modName) {
			fmt.Printf("%s - %s\n", modName, modDesc)
		}
	}

	return nil
}

func (db *Database) findModFile(name, mcvsn string) (string, error) {
	// First, look up the modid for the given name
	var modid int
	err := db.sqlDb.QueryRow("select rowid from mods where name = ?", name).Scan(&modid)
	switch {
	case err == sql.ErrNoRows:
		return "", fmt.Errorf("No mod found %s", name)
	case err != nil:
		return "", err
	}

	// Now find the latest release or beta version
	var url string
	err = db.sqlDb.QueryRow("select url from files where rowid in (select fileid from filevsns where modid=? and version=? order by tstamp desc limit 1)",
		modid, mcvsn).Scan(&url)
	switch {
	case err == sql.ErrNoRows:
		return "", fmt.Errorf("No file found for %s on Minecraft %s", name, mcvsn)
	case err != nil:
		return "", err
	}
	return url, nil
}

func (db *Database) findModFileByUrl(url, mcvsn string) (string, error) {
	// First, look up the modid for the given name
	var modid int
	err := db.sqlDb.QueryRow("select rowid from mods where url = ?", url).Scan(&modid)
	switch {
	case err == sql.ErrNoRows:
		return "", fmt.Errorf("No mod found %s", url)
	case err != nil:
		return "", err
	}

	// Now find the latest release or beta version
	var fileUrl string
	err = db.sqlDb.QueryRow("select url from files where rowid in (select fileid from filevsns where modid=? and version=? order by tstamp desc limit 1)",
		modid, mcvsn).Scan(&fileUrl)
	switch {
	case err == sql.ErrNoRows:
		return "", fmt.Errorf("No file found for %s on Minecraft %s", url, mcvsn)
	case err != nil:
		return "", err
	}
	return url, nil
}
