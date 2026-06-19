package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func PreconditionFailed(c *gin.Context, message string) {
	Json(c, http.StatusPreconditionFailed, gin.H{"message": message})
}
func TokenExpires(c *gin.Context, message string) {
	Json(c, 419, gin.H{"message": message})
}
func BadRequest(c *gin.Context, message string) {
	Json(c, http.StatusBadRequest, gin.H{"message": message})
}
func Unauthorized(c *gin.Context, message string) {
	Json(c, http.StatusUnauthorized, gin.H{"message": message})
}
func Success(c *gin.Context, data any) {
	Json(c, http.StatusOK, data)
}
func ServerError(c *gin.Context, message string) {
	log.Error("服务器错误: ", message)
	Json(c, http.StatusInternalServerError, gin.H{"message": message})
}

func ServiceUnavailable(c *gin.Context, message string) {
	Json(c, http.StatusServiceUnavailable, gin.H{"message": message})
}
func Json(c *gin.Context, status int, data any) {
	c.JSON(status, data)
	c.Abort()
}
