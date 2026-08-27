-- 0017_ca_certificates: certificates this host trusts in addition to the
-- system roots.
--
-- The problem this exists for: an integration inside a company reaches an
-- HTTPS address whose certificate was issued by that company's own authority,
-- or by the appliance itself. Nothing in a public trust store has heard of
-- either, so the connection fails at the handshake -- and the two ways out
-- without this table are both bad. Disabling verification sends a credential
-- to whatever answers on that address. Mounting a bundle into the container's
-- system trust store widens trust for every outbound connection the process
-- makes, and lives in a compose file that nobody looking at the dashboard
-- would think to read.
--
-- Named entries rather than one bundle blob. A bundle can be stored in a
-- setting; what it cannot do is say which of the three certificates inside it
-- is the one expiring in six weeks. The facts below are parsed once, when the
-- certificate is added, so the page can name a subject, show an expiry and
-- refuse an unreadable paste at the point somebody pastes it rather than at
-- the first connection months later.
--
-- Nothing here is a secret. A certificate is the public half by construction:
-- it is presented to every client that connects to the server it belongs to.
-- So it is stored in the clear, unlike `plugins.<instance>.token`, and is
-- shown in full in the dashboard, which is what makes a wrong one debuggable.
-- The security-relevant fact is not the bytes, it is the decision to trust
-- them -- which is why adding one and pointing an instance at one are both
-- audited.
CREATE TABLE ca_certificates (
    id          TEXT    PRIMARY KEY,
    -- What an operator types when attaching this to an instance, and reads on
    -- the Certificates page. Unique case-insensitively, for the same reason
    -- groups.name is: two entries called "Work CA" and "work ca" are one
    -- certificate as far as anybody looking at the list is concerned.
    name        TEXT    NOT NULL,
    -- The certificate itself, PEM-encoded. Always PEM in this column whatever
    -- was uploaded: a DER file is converted on the way in, so everything that
    -- reads this column reads one shape.
    pem         TEXT    NOT NULL,

    -- Facts parsed from the certificate when it was added, not typed by
    -- anybody. They are a cache of what `pem` already says, kept because a
    -- listing that had to parse every row to render would parse them on every
    -- page load, and because sorting by expiry in SQL is what makes "what is
    -- about to break" answerable.
    subject     TEXT    NOT NULL,
    issuer      TEXT    NOT NULL,
    -- SHA-256 of the DER, lowercase hex. Unique: the same certificate under
    -- two names would be two rows describing one trust decision, and revoking
    -- trust would then mean finding both.
    fingerprint TEXT    NOT NULL,
    not_before  INTEGER NOT NULL,
    not_after   INTEGER NOT NULL,
    -- What the certificate says about being an authority, and whether it said
    -- anything at all. The pair is stored rather than collapsed into one
    -- column because they answer different questions: a certificate carrying
    -- `basicConstraints: CA:FALSE` cannot anchor a chain, while one carrying
    -- no basicConstraints at all can -- and that second case is the appliance
    -- certificate this feature mostly exists for. Collapsing them would warn
    -- about every one of those.
    is_ca       INTEGER NOT NULL,
    basic_constraints_valid INTEGER NOT NULL,
    -- The keyUsage bits, as x509.KeyUsage. Zero means the certificate named
    -- no usage, which constrains nothing; a usage that omits certificate
    -- signing is the other way a certificate cannot anchor a chain.
    key_usage   INTEGER NOT NULL,

    added_by    TEXT    NOT NULL,
    added_at    INTEGER NOT NULL,
    CHECK (name <> ''),
    CHECK (length(name) <= 64),
    CHECK (is_ca IN (0, 1)),
    CHECK (basic_constraints_valid IN (0, 1))
) STRICT;

CREATE UNIQUE INDEX ux_ca_certificates_name ON ca_certificates (lower(name));
CREATE UNIQUE INDEX ux_ca_certificates_fingerprint ON ca_certificates (fingerprint);
