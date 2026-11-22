# Demo Audio Licenses

Colton owns music metadata and license verification.
Only add MP3s when licensing explicitly permits the intended use and redistribution.
For each track, `assets/music/catalog.json` must include:

- stable track ID
- title and artist
- duration in milliseconds
- exact MP3 filename
- source URL
- license name and license URL
- required attribution text

A filename or "no copyright" description alone is insufficient proof.

## Lost in a Dream

- **Artist:** Eternity Arcade
- **Title:** Lost in a Dream
- **Source:** https://commons.wikimedia.org/wiki/File:Eternity_Arcade_-_Lost_in_a_Dream_(vocal_retro_pop_music_made_with_AI).opus
- **License:** [Creative Commons Attribution 4.0 International](https://creativecommons.org/licenses/by/4.0/)
- **Attribution:** "Lost in a Dream" by Eternity Arcade. Audio extracted from the creator's video and transcoded to OPUS/MP3 via Wikimedia Commons; licensed as CC BY 4.0 on the Commons file page. Source and license links above. No endorsement implied.
- **Asset:** `Eternity_Arcade_-_Lost_in_a_Dream_(vocal_retro_pop_music_made_with_AI).mp3`
- **Modification:** Wikimedia MP3 derivative of the source OPUS file

### Evidence checked on 2026-11-22

- [Creator's original video](https://www.youtube.com/watch?v=uUWWPk69JWs), published 2024-05-18: its indexed description includes the user's quotation, "Eternity Arcade videos are available to reuse under a Creative Commons licence."
- [Commons file-page revision](https://commons.wikimedia.org/w/index.php?title=File:Eternity_Arcade_-_Lost_in_a_Dream_(vocal_retro_pop_music_made_with_AI).opus&oldid=1233913062) identifies that same video as its source, names Eternity Arcade as author, and labels this audio CC BY 4.0. It also lists an MP3 transcode.
- [CC BY 4.0 terms](https://creativecommons.org/licenses/by/4.0/) permit redistribution and adaptation with credit, a license link, and an indication of changes. Retain the extraction/transcoding notice and attribution with redistributed copies.

The creator's sentence establishes a reuse statement for the video but does not
specify a CC version. The CC BY 4.0 designation is from Commons, not from that
sentence. Commons still marks external-source license review as pending; this
record does not claim that review is complete or that the creator's original
license selector was independently verified. Direct retrieval of the Commons
MP3 returned HTTP 403 during this check, so the bundled file was not independently
byte-matched to its claimed Wikimedia transcode.

Bundled MP3 SHA-256 (identifies the reviewed repository asset):
`0fb15c502297c8caf58688cac13247b7ae2139a29236380d17bba76d2e261d36`.

The MP3 was supplied by Colton's feature commit, not downloaded or re-encoded
during this review. Confirm the exact asset's provenance and the original
license/version before treating this record as completed license verification.
