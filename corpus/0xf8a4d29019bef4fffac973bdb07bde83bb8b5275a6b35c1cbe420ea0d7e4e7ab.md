---
request: 0xf8a4d29019bef4fffac973bdb07bde83bb8b5275a6b35c1cbe420ea0d7e4e7ab
outcome: secure
rules: R-4.3
chain: 1
safe: 0x14326E0664631430fe7187C826041E732629DEdD
charter: bafkreigg6ajqdb52kb644wcxegurt7r6jlclru4mjy3lwxkbh6avm3vgre
proposed: 2026-11-24T13:18:50Z
record: 0x74257d214ed898952641a08535edf1d303bd4dc6c1a79bb0b5e63d802feb706a
---

# Ruling on request 0xf8a4d29019bef4fffac973bdb07bde83bb8b5275a6b35c1cbe420ea0d7e4e7ab

## Proposal

- Oracle: 0x4F61B8832978e83b80D69551AEf07557DBE41d03
- Consensus: 0xc855761D619f6002923507cE68B84d7689C2aa96
- Charter name: "charter.safenet-gov.eth"
- Gnosis Chain block: 49445299 (2026-11-24T13:18:50Z)
- Gnosis Chain transaction: 0x701ccb892fb3081e0b59c017687c9054abffd75fcaeea4289ea52975d889ee4e
- Ethereum Mainnet block before: 26485269
- Safe chain block before: 26485269
- Epoch: 34337
- Oracle data: 0x
- Safe transaction hash: 0xa1d6a691bfcc7308e7d1082490825ccd347cab34c0c9005a1d8dc62971d83945

## Safe transaction

- Chain ID: 1
- Safe: 0x14326E0664631430fe7187C826041E732629DEdD
- To: 0x9A4f91Ce55FB9CEAB0205A3F817e3b700dF2925e
- Value: 0
- Data: 0x5cfe2fe400000000000000000000000000000000000000000000000000d2a2c813759bed
- Operation: CALL
- Safe tx gas: 0
- Base gas: 0
- Gas price: 0
- Gas token: 0x0000000000000000000000000000000000000000
- Refund receiver: 0x0000000000000000000000000000000000000000
- Nonce: 10

## Votes

- 0xc84a0928632382E75c543ACCeD101B9525027362: DENIED, reason "R-4.3"
- 0x101d59DBB8C5e70f45D52634f23800fCa4Fbe031: APPROVED

## Ruling

- Outcome: secure
- Frozen: block 49445306
- Deadline: block 49929146
- Record: block 49477793, transaction 0x74257d214ed898952641a08535edf1d303bd4dc6c1a79bb0b5e63d802feb706a
- Context: the document below

<!-- DOCUMENT START -->
---

---
precedents: 0xb839c64408975c6ca5848b5e28bd95c2b82b0a28997404b4f694099726eeca1f, 0x586a2ed7fc5cbbe9043a34fb9d9d81a7f46b66740a84767c8515b782b97cf6eb, 0x6b655b88b3e9ea11cdeb69a34b375ad673ac3907670843acc8100c5974c838ed, 0x0263bd9a76793c3bdd19e1ac7718ba4646e190fc97661519bf20f41194f3e3c3, 0x844fe527e82fbf88b70e05db4c487de8143ca5d3b97e2eca24f6348c4d894434
tags: vault-redeem-request, own-shares-to-escrow, no-recipient-in-calldata, unverified-proxy, verified-implementation, whitelist-manager
summary: Vault claimSharesAndRequestRedeem moving the Safe's own shares into the vault's verified pending silo, with claims to the Safe and no receiver named; secure under R-4.3.
---

Secure under R-4.3 (Charter bafkreigg6ajqdb52kb644wcxegurt7r6jlclru4mjy3lwxkbh6avm3vgre). Safe 0x14326e0664631430fe7187c826041e732629dedd on Ethereum Mainnet calls claimSharesAndRequestRedeem(uint256) on the vault 0x9a4f91ce55fb9ceab0205a3f817e3b700df2925e, whose implementation 0xe50554ec802375c9c3f9c087a8a7bb8c26d3dedf is Sourcify-verified. The call sends no native value and names no recipient. By the verified code, its claims go to the Safe, and it moves the Safe's shares to the vault's pending silo 0x65e2824c1004d3db11940cf798980630a2f172ad, a verified Silo with no function that pays out to an account, identified from the vault's settlement logs before block 26485269. The Safe holds the vault token, received 14 mints of it, and held the vault's whitelist-manager role, so the silo is inside the expected target set (§ 2.4). The vault's unread storage is an absence of evidence, not determinative alone (§ 3.8). Applies 0xb839c644…, 0x586a2ed7… and 0x6b655b88… (secure); distinguishes 0x0263bd9a… and 0x844fe527… (insecure). Novel pattern, flagged for SafeDAO (§ 5.1).