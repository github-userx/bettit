package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type Thread = []Comment

type Comment struct {
	id      int
	title   string
	content string
	replies []Comment
}

type ThreadError struct {
	code    int
	message string
}

func (err *ThreadError) Error() string {
	return err.message
}

func (err *ThreadError) Code() int {
	return err.code
}

func getThread(req *http.Request, c *gin.Context) ([]byte, *ThreadError) {

	client := http.Client{
		Timeout: time.Second * 5,
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, &ThreadError{code: http.StatusBadRequest, message: err.Error()}
	}

	if res.Body != nil {
		defer res.Body.Close()
	}

	if res.StatusCode != 200 {
		log.Printf(fmt.Sprintf("Bad response to request at: %s. Status: %s", req.URL.Path, res.Status))
		return nil, &ThreadError{code: res.StatusCode, message: "Recieved unsuccessful response from Reddit API."}
	}

	body, readErr := ioutil.ReadAll(res.Body)
	if readErr != nil {
		return nil, &ThreadError{code: http.StatusBadRequest, message: err.Error()}
	}

	return body, nil
}

func GettitRouter() *gin.Engine {

	r := gin.Default()
	r.GET("/health", func(c *gin.Context) {
		c.String(http.StatusOK, "API is live.")
	})
	r.POST("/archive", func(c *gin.Context) {

		subreddit := c.Query("sub")
		id := c.Query("id")
		thread := c.Query("thread")

		requestUrl := fmt.Sprintf("https://reddit.com/r/%s/comments/%s/%s.json", subreddit, id, thread)
		req, err := http.NewRequest(http.MethodGet, requestUrl, nil)

		req.Header.Set("User-Agent", "bettit-archive")

		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}

		threadBytes, tErr := getThread(req, c)
		if tErr != nil {
			c.JSON(tErr.Code(), gin.H{
				"message": tErr.Error(),
			})
			return
		}

		go txPostThread("", "", threadBytes)

		c.JSON(http.StatusOK, gin.H{"message": "Archive created."})
	})
	return r
}
