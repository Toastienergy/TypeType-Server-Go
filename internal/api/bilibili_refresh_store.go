package api

import (
	"context"
	"time"
)

// refreshBiliSession is BiliBiliSessionRefreshService.refresh: a rotation awaiting confirmation is confirmed first;
// otherwise the cookies are inspected and rotated, the new ones stored with the old token pending confirmation.
func (s *Server) refreshBiliSession(ctx context.Context, l biliLease) {
	c := s.biliCrypto()
	cookies, err := c.decrypt(l.cookies)
	if err != nil {
		s.biliLeaseReconnect(ctx, l)
		return
	}
	if l.hasPending {
		pending, err := c.decrypt(l.pending)
		if err != nil {
			s.biliLeaseReconnect(ctx, l)
			return
		}
		s.finishBiliConfirm(ctx, l, cookies, pending)
		return
	}
	token, err := c.decrypt(l.token)
	if err != nil {
		s.biliLeaseReconnect(ctx, l)
		return
	}
	result, newCookies, newToken, expires := biliRotate(ctx, cookies, token)
	switch result {
	case refreshNotRequired:
		s.biliLeaseDone(ctx, l)
	case refreshReconnect:
		s.biliLeaseReconnect(ctx, l)
	case refreshRotated:
		encCookies, encToken, encOld := c.encrypt(newCookies), c.encrypt(newToken), c.encrypt(token)
		now := time.Now().UnixMilli()
		tag, err := s.DB.Exec(ctx, `UPDATE bilibili_sessions SET encrypted_cookies = $4, encrypted_refresh_token = $5,
encrypted_pending_confirm_token = $6, expires_at = $7, updated_at = $8, refresh_checked_at = $8
WHERE user_id = $1 AND refresh_claimed_until = $2 AND status = $3`, l.user, l.until, "connected", encCookies, encToken, encOld, expires, now)
		if err != nil || tag.RowsAffected() == 0 {
			return
		}
		s.finishBiliConfirm(ctx, l, newCookies, token)
	default:
		s.biliLeaseRelease(ctx, l)
	}
}

func (s *Server) finishBiliConfirm(ctx context.Context, l biliLease, cookies, oldToken string) {
	switch biliConfirm(ctx, cookies, oldToken) {
	case refreshConfirmed:
		s.biliLeaseDone(ctx, l)
	case refreshReconnect:
		s.biliLeaseReconnect(ctx, l)
	default:
		s.biliLeaseRelease(ctx, l)
	}
}

const biliLeaseWhere = " WHERE user_id = $1 AND refresh_claimed_until = $2 AND status = 'connected'"

func (s *Server) biliLeaseDone(ctx context.Context, l biliLease) {
	s.DB.Exec(ctx, "UPDATE bilibili_sessions SET refresh_checked_at = $3, refresh_claimed_until = 0, encrypted_pending_confirm_token = NULL"+biliLeaseWhere,
		l.user, l.until, time.Now().UnixMilli())
}

func (s *Server) biliLeaseReconnect(ctx context.Context, l biliLease) {
	s.DB.Exec(ctx, `UPDATE bilibili_sessions SET status = 'needs_reconnect', updated_at = $3, encrypted_refresh_token = NULL,
encrypted_pending_confirm_token = NULL, refresh_claimed_until = 0`+biliLeaseWhere, l.user, l.until, time.Now().UnixMilli())
}

func (s *Server) biliLeaseRelease(ctx context.Context, l biliLease) {
	s.DB.Exec(ctx, "UPDATE bilibili_sessions SET refresh_claimed_until = 0"+biliLeaseWhere, l.user, l.until)
}
