# Passwords

Your passwords on all your devices under one **master password** (not stored
anywhere). There is one vault per device network: on another device
«Passwords» finds it («Your network has a password vault») — enter the same
master password and it opens there; entries then sync over the device network.
«Create a new vault» makes a **separate** vault — usually not what you want.
The vault is encrypted (AES-256).

- **Two vaults in one network** (made before): the open vault shows «Another
  password vault in your network» — enter that vault's master password →
  «Merge». Entries of both end up here; devices with the other vault switch to
  this one by themselves on their next sync and then open with this vault's
  master password.

- **Phone**: choose Tarnegis as the autofill service — your logins appear in
  any app's login fields; new passwords are offered for saving.
- **PC browser**: «Install extension» → load it unpacked in
  browser://extensions (developer mode) → paste the pairing key.
- **Import / export**: export passwords from the browser as CSV
  (chrome://password-manager/settings, Yandex: browser://password-manager/settings)
  → «Import CSV». «Export CSV» (asks for the master password) saves all
  entries to Downloads in Chrome format. Delete the CSV afterwards.
