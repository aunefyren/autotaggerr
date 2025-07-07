package routers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func APIPing(context *gin.Context) {
	context.JSON(http.StatusOK, gin.H{"message": "Hello world!"})
}
