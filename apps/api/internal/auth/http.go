package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

const SessionCookieName = "vigira_session"

type SetupStatusOutput struct {
	Body struct {
		SetupRequired bool `json:"setupRequired"`
	}
}

type FirstUserInput struct {
	Body struct {
		Username        string `json:"username" minLength:"3" maxLength:"64"`
		DisplayName     string `json:"displayName" maxLength:"80"`
		Password        string `json:"password" minLength:"12" maxLength:"1024"`
		ConfirmPassword string `json:"confirmPassword" minLength:"12" maxLength:"1024"`
	}
}

type LoginInput struct {
	Body struct {
		Username string `json:"username" minLength:"3" maxLength:"64"`
		Password string `json:"password" minLength:"1" maxLength:"1024"`
	}
}

type LogoutInput struct {
	Cookie string `header:"Cookie"`
}

type UserOutput struct {
	Body struct {
		User User `json:"user"`
	}
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type LogoutOutput struct {
	Body struct {
		LoggedOut bool `json:"loggedOut"`
	}
	SetCookie http.Cookie `header:"Set-Cookie"`
}

func RegisterAPI(api huma.API, service *Service, secureCookie bool) {
	huma.Register(api, huma.Operation{
		OperationID: "getSetupStatus", Method: http.MethodGet, Path: "/api/setup/status",
		Summary: "Check whether initial setup is required", Tags: []string{"Setup"},
	}, func(ctx context.Context, _ *struct{}) (*SetupStatusOutput, error) {
		required, err := service.SetupRequired(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("Could not read setup state")
		}
		out := &SetupStatusOutput{}
		out.Body.SetupRequired = required
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createFirstUser", Method: http.MethodPost, Path: "/api/setup/first-user",
		Summary: "Create the first local user", Tags: []string{"Setup"},
	}, func(ctx context.Context, input *FirstUserInput) (*UserOutput, error) {
		session, err := service.CreateFirstUser(ctx, input.Body.Username, input.Body.DisplayName, input.Body.Password, input.Body.ConfirmPassword)
		if err != nil {
			return nil, setupError(err)
		}
		return userOutput(session, secureCookie), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/api/auth/login",
		Summary: "Log in with a local account", Tags: []string{"Authentication"},
	}, func(ctx context.Context, input *LoginInput) (*UserOutput, error) {
		session, err := service.Login(ctx, input.Body.Username, input.Body.Password)
		if err != nil {
			if errors.Is(err, ErrInvalidCredentials) {
				return nil, huma.Error401Unauthorized("Invalid username or password")
			}
			return nil, huma.Error500InternalServerError("Could not create login session")
		}
		return userOutput(session, secureCookie), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: "/api/auth/logout",
		Summary: "Log out the current local account", Tags: []string{"Authentication"},
	}, func(ctx context.Context, input *LogoutInput) (*LogoutOutput, error) {
		request := &http.Request{Header: make(http.Header)}
		request.Header.Set("Cookie", input.Cookie)
		cookie, _ := request.Cookie(SessionCookieName)
		token := ""
		if cookie != nil {
			token = cookie.Value
		}
		if err := service.Logout(ctx, token); err != nil {
			return nil, huma.Error500InternalServerError("Could not end login session")
		}
		return &LogoutOutput{
			SetCookie: sessionCookie("", time.Unix(0, 0), secureCookie, true),
			Body: struct {
				LoggedOut bool `json:"loggedOut"`
			}{LoggedOut: true},
		}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getCurrentUser", Method: http.MethodGet, Path: "/api/auth/me",
		Summary: "Get the current local user", Tags: []string{"Authentication"},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			User User `json:"user"`
		}
	}, error) {
		user, authErr, ok := userFromContext(ctx)
		if authErr != nil {
			return nil, huma.Error500InternalServerError("Could not validate login session")
		}
		if !ok {
			return nil, huma.Error401Unauthorized("Login required")
		}
		out := &struct {
			Body struct {
				User User `json:"user"`
			}
		}{}
		out.Body.User = user
		return out, nil
	})
}

func userOutput(session Session, secure bool) *UserOutput {
	out := &UserOutput{}
	out.Body.User = session.User
	out.SetCookie = sessionCookie(session.Token, session.ExpiresAt, secure, false)
	return out
}

func sessionCookie(value string, expires time.Time, secure, clear bool) http.Cookie {
	cookie := http.Cookie{
		Name: SessionCookieName, Value: value, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode,
	}
	if clear {
		cookie.MaxAge = -1
		cookie.Expires = expires
	} else {
		cookie.Expires = expires
		cookie.MaxAge = int(time.Until(expires).Seconds())
	}
	return cookie
}

func setupError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, ErrSetupComplete):
		return huma.Error409Conflict("Initial setup is already complete")
	case errors.Is(err, ErrUsernameTaken):
		return huma.Error409Conflict("That username is already in use")
	default:
		return huma.Error500InternalServerError("Could not complete initial setup")
	}
}
