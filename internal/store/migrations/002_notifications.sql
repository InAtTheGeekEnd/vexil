-- The expiry time of the certificate the warning was sent for, so a
-- restart does not send it again. A renewed certificate has a new expiry.
ALTER TABLE monitors ADD COLUMN cert_warned_at INTEGER;

-- The last failed delivery, shown on the Notifications page.
ALTER TABLE channels ADD COLUMN last_error TEXT;
ALTER TABLE channels ADD COLUMN last_error_at INTEGER;
