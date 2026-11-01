# Protocol

Firmware 4.93. VSH performs every score request on behalf of the game.

1. `GET http://static-resource.np.community.playstation.net/np/resource/title/<commid>/ranking/<commid>-ranking.xml`
   returns `<ranking status="alive" titleid="<commid>"><record-fqdn>…</record-fqdn><view-fqdn>…</view-fqdn></ranking>`,
   preceded by the `<!--Sce-Np-Signature: …-->` HMAC-SHA1 line (`np-common/resource`), keyed with the
   ranking resource key. The console caches it for 60 seconds.
2. `POST https://<record-fqdn>/ranking_update/sec/{record_score,record_data}` and
   `POST http://<view-fqdn>/ranking_view/func/<op>` (friend ranking over https under `/ranking_view/sec/`).
   Bodies are XML rooted at `<ranking platform="ps3" sv="4.93">`; secure calls carry `<ticket>` (base64)
   and `<ticketjid>`. No port is added, so the hosts are reached on 80 and 443.
3. Replies are HTTP 200 with a `Content-Length` and `<ranking result="XX">`; `00` is success, any other
   two-hex value is the error the game sees (`02` bad ticket, `15` not best score, `18` no game data,
   `23` game data for a score the player does not hold).
4. View replies need `Sce-Np-Signature: hex(HMAC-SHA1(title passphrase, XML part))`. The result is checked
   first, so errors pass unsigned. Record replies are not checked.
5. `record_data` and `get_game_data` carry raw bytes after the XML: XML, `\r\n\r\n`, data, with
   `Sce-Np-Info-Length` giving the XML length. Only the XML part is signed.
6. `get_player_rank` must return at least one `<record>`; when nobody asked for is ranked, the server sends
   one for a player that matches none of them, which leaves every slot "not ranked". That reply is inferred.

The `anticheat` element of `record_score` is not checked. Friend ranking lists each player's best pcId with
their global rank.
