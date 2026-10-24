package agents

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/utility"
)

// callerID returns the authenticated caller's user id. When it cannot be read it writes the
// error response itself and returns false, so the handler only has to return.
func (base *Controller) callerID(c *gin.Context) (string, bool) {
	value, err := middleware.GetUserClaims(c, base.Db.Postgresql, "user_id")
	if err != nil {
		base.Logger.Info("unable to fetch user claims")
		rd := utility.BuildErrorResponse(http.StatusUnauthorized, "error", "authentication required", err, nil)
		c.JSON(http.StatusUnauthorized, rd)
		return "", false
	}

	userID, ok := value.(string)
	if !ok {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "user_id is not of type string", nil, nil)
		c.JSON(http.StatusBadRequest, rd)
		return "", false
	}

	return userID, true
}
