# Optional three-song presentation

This demo branch extends the presentation catalog after submission. The default
`go run ./cmd/demo` still uses the submitted one-song catalog. No changes to the
report, shared protocol or default catalog are needed.

## Prepare your laptop once

Keep your own downloaded MP3s in Downloads. They are not included in this branch:
both publishers restrict redistribution of standalone audio files. The two local
asset paths are ignored by Git. Do not force-add them or upload them to GitHub.

In an existing checkout:

```sh
git fetch origin
git switch demo/three-song-catalog
```

On Mac, from the RoomPlay folder:

```sh
cp "$HOME/Downloads/bensound-loveofthesun.mp3" assets/music/
cp "$HOME/Downloads/Emotional-Piano-Free-Royalty-Free-Relaxing-Music-by-Liborio-Conti-01-I-Believe.mp3" assets/music/
```

On Windows PowerShell, from the RoomPlay folder:

```powershell
Copy-Item -LiteralPath "$env:USERPROFILE/Downloads/bensound-loveofthesun.mp3" -Destination assets/music/
Copy-Item -LiteralPath "$env:USERPROFILE/Downloads/Emotional-Piano-Free-Royalty-Free-Relaxing-Music-by-Liborio-Conti-01-I-Believe.mp3" -Destination assets/music/
```

## Run the presentation (Mac and Windows)

Stop any previous launcher with Ctrl+C, then run:

```sh
go run ./cmd/demo -catalog assets/music/catalog-demo.json
```

All three tracks appear in the catalog and every room supports real audio. Try
Lost in a Dream in Bedroom, Love of the Sun in Kitchen, and I Believe in Living
Room. Queue different tracks, then use Next/Previous to show independent queues.
The Bensound track card displays the supplied credit and license code.

Audio preparation may exceed the response deadline: wait for state to update
instead of clicking repeatedly. Rehearse on the actual laptop and prepare/pause
the rooms before presenting. No new claim of Mac audio runtime verification is made.

## Source and credit records

- **Love of the Sun — Indie Box.** User-supplied Bensound download and license code.
  Display exactly: “Royalty Free Music: Bensound.com”,
  “License code: OAQUIDWZLYJYUWUW”, “Artist: Indie Box”.
  [Publisher terms](https://www.bensound.com/terms-and-conditions).
- **I Believe — Liborio Conti.** User-supplied download. The artist's
  [license](https://www.no-copyright-music.com/wp-content/uploads/2023/03/LiborioContiLicense.pdf)
  says attribution is not required, but restricts redistribution of the music files.
  The artist name remains ordinary catalog metadata.
- **Lost in a Dream — Eternity Arcade.** The bundled source and license record
  remains in [ATTRIBUTION.md](../assets/music/ATTRIBUTION.md).

These records do not establish that a Bensound attribution code alone licenses
every use of a music-player demonstration. Retain the applicable download license
and confirm its scope for your presentation. No additional distribution rights
are asserted by this branch.
