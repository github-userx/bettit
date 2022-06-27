package main

import (
	"database/sql"
	"fmt"
	"html"
	"html/template"
	"io/ioutil"
	"log"
	"strings"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"github.com/tidwall/gjson"
)

var db *sql.DB
var templates *template.Template

type DbError struct {
	message string
}

// Thread type used in templates.
//
type ThreadTmpl struct {
	ThreadTitle   string
	ThreadContent template.HTML
	Replies       []CommentTmpl
}

// Comment type used in templates.
//
type CommentTmpl struct {
	CommentId      string
	CommentContent template.HTML
	Children       []CommentTmpl
}

func (err *DbError) Error() string {
	return err.message
}

func LoadTemplates() {
	var allFiles []string
	files, err := ioutil.ReadDir("./templates")
	if err != nil {
		fmt.Println(err)
	}
	for _, file := range files {
		filename := file.Name()
		if strings.HasSuffix(filename, ".tmpl") {
			allFiles = append(allFiles, "./templates/"+filename)
		}
	}
	templates, err = template.ParseFiles(allFiles...)
}

func InitDatabase() {
	openedDb, errOpen := sql.Open("sqlite3", "./bettit.db")
	db = openedDb
	if errOpen != nil {
		log.Fatalf("Error opening database: %s", errOpen.Error())
	}

	if statement, err := db.Prepare(`
		CREATE TABLE IF NOT EXISTS threads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			thread_id TEXT,
			timestamp INTEGER,
			sub TEXT,
			title TEXT,
			content TEXT,
			author TEXT,
			CONSTRAINT unq UNIQUE(thread_id, timestamp)
		);`,
	); err != nil {
		log.Fatalf("Error creating threads table: %s", err.Error())
	} else {
		statement.Exec()
	}

	if statement, err := db.Prepare(`
		CREATE TABLE IF NOT EXISTS comments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT,
			author TEXT,
			thread_id INTEGER,
			parent_id INTEGER,
			FOREIGN KEY (thread_id) REFERENCES threads(id),
			FOREIGN KEY (parent_id) REFERENCES comments(id)
		);`,
	); err != nil {
		log.Fatalf("Error creating comments table: %s", err.Error())
	} else {
		statement.Exec()
	}

	if statement, err := db.Prepare(`
		CREATE INDEX IF NOT EXISTS threads_id_index ON threads(thread_id)
	`); err != nil {
		log.Fatalf("Error creating database index: %s", err.Error())
	} else {
		statement.Exec()
	}

	LoadTemplates()
}

// Post a new comment to the database and all replies to it.
// Recursive function.
//
func txPostComment(
	tx *sql.Tx,
	data gjson.Result,
	threadId string,
	parent int,
	currentDepth int,
	maxDepth int) (*CommentTmpl, error) {

	if currentDepth == maxDepth {
		return nil, nil
	}

	content := data.Get("data.body_html").String()
	author := data.Get("data.author").String()
	parentStr := "NULL"
	if parent > -1 {
		parentStr = fmt.Sprintf("%d", parent)
	}

	var insertId int64 = -1
	if statement, err := tx.Prepare(`
		INSERT INTO comments ( content, author, thread_id, parent_id )
		VALUES ( ?, ?, ?, ? );
	`); err != nil {
		return nil, &DbError{
			fmt.Sprintf("Error creating a new comment: %s", err.Error()),
		}
	} else {
		result, exErr := statement.Exec(content, author, threadId, parentStr)
		if exErr != nil {
			return nil, &DbError{
				fmt.Sprintf("Error creating a new comment: %s", err.Error()),
			}
		}
		insertId, _ = result.LastInsertId()
	}
	replies := data.Get("data.replies.data.children")
	repliesTmpl := []CommentTmpl{}
	if replCount := replies.Get("#").Int(); replies.Exists() && replies.IsArray() && replCount > 0 {
		for i := 0; i < int(replCount); i++ {
			reply, bubbledError := txPostComment(
				tx,
				replies.Get(fmt.Sprintf("%d", i)),
				threadId,
				int(insertId),
				currentDepth+1,
				maxDepth,
			)
			if bubbledError != nil {
				return nil, bubbledError
			}
			repliesTmpl = append(repliesTmpl, *reply)
		}
	}

	return &CommentTmpl{
		fmt.Sprintf("reply-%d", insertId),
		template.HTML(html.UnescapeString(content)),
		repliesTmpl,
	}, nil
}

// Post a new thread to the database and create a corresponding HTML file.
// Returns the created html to send as a repsonse.
//
// TODO: on error handling, only append internal details if in debug mode
//
func txPostThread(sub string, data []byte, writer *gin.ResponseWriter) (*template.Template, error) {

	if db == nil {
		panic("Database not open!")
	}

	tx, txErr := db.Begin()
	if txErr != nil {
		return nil, &DbError{
			fmt.Sprintf("Error starting transaction on a new thread: %s", txErr.Error()),
		}
	}

	// Add first post as thread.
	thrBody := gjson.GetBytes(data, "0.data.children.0.data.selftext_html").String()
	thrId := gjson.GetBytes(data, "0.data.children.0.data.id").String()
	thrTitle := gjson.GetBytes(data, "0.data.children.0.data.title").String()
	thrAuthor := gjson.GetBytes(data, "0.data.children.0.data.author").String()

	thrStmnt, stmntErr := tx.Prepare(`
		INSERT INTO threads ( thread_id, timestamp, title, content, author, sub )
		VALUES ( ?, ?, ?, ?, ?, ? );
	`)

	if stmntErr != nil {
		tx.Rollback()
		return nil, &DbError{
			fmt.Sprintf("Error preparing new thread insert query: %s", stmntErr.Error()),
		}
	}

	_, thrExcErr := thrStmnt.Exec(thrId, 0, thrTitle, thrBody, thrAuthor, sub)

	if thrExcErr != nil {
		tx.Rollback()
		return nil, &DbError{
			fmt.Sprintf("Error executing new thread insert query: %s", thrExcErr.Error()),
		}
	}

	log.Printf("Created a new thread. ID %s", thrId)
	replies := []CommentTmpl{}

	comments := gjson.GetBytes(data, "1.data.children")
	for i := 0; i < int(comments.Get("#").Int()); i++ {
		reply, bubbledError := txPostComment(tx, comments.Get(fmt.Sprintf("%d", i)), thrId, -1, 0, 100)
		if bubbledError != nil {
			tx.Rollback()
			return nil, bubbledError
		}
		replies = append(replies, *reply)
	}

	tx.Commit()

	t := templates.Lookup("thread.tmpl").Lookup("thread")

	t.Execute(*writer, ThreadTmpl{
		thrTitle,
		template.HTML(html.UnescapeString(thrBody)),
		replies,
	})

	return nil, nil
}
