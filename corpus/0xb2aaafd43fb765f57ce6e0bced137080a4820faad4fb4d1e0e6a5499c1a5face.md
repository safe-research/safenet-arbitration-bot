---
request: 0xb2aaafd43fb765f57ce6e0bced137080a4820faad4fb4d1e0e6a5499c1a5face
outcome: secure
rules: R-4.3
chain: 1
safe: 0x3579D761025F9c755f997DFA7749775B0AdEbae7
charter: bafkreigg6ajqdb52kb644wcxegurt7r6jlclru4mjy3lwxkbh6avm3vgre
proposed: 2026-11-24T11:58:45Z
record: 0x74257d214ed898952641a08535edf1d303bd4dc6c1a79bb0b5e63d802feb706a
---

# Ruling on request 0xb2aaafd43fb765f57ce6e0bced137080a4820faad4fb4d1e0e6a5499c1a5face

## Proposal

- Oracle: 0x4F61B8832978e83b80D69551AEf07557DBE41d03
- Consensus: 0xc855761D619f6002923507cE68B84d7689C2aa96
- Charter name: "charter.safenet-gov.eth"
- Gnosis Chain block: 49444354 (2026-11-24T11:58:45Z)
- Gnosis Chain transaction: 0x02ef1beb4489b1317a72c6f0cee8eb83eac260e6a6fcfba16200555a1b4a74dc
- Ethereum Mainnet block before: 26484870
- Safe chain block before: 26484870
- Epoch: 34336
- Oracle data: 0x
- Safe transaction hash: 0x018b1b0ecab6dcf891e2ceea215bc2ad3f403628ba2e2a37a02789ae3341c9a1

## Safe transaction

- Chain ID: 1
- Safe: 0x3579D761025F9c755f997DFA7749775B0AdEbae7
- To: 0xeC5dfbB37bF296980D15086b6EDfbd12D2836E50
- Value: 0
- Data: 0x5cfe2fe400000000000000000000000000000000000000000000001d753b1c5702ea7488
- Operation: CALL
- Safe tx gas: 0
- Base gas: 0
- Gas price: 0
- Gas token: 0x0000000000000000000000000000000000000000
- Refund receiver: 0x0000000000000000000000000000000000000000
- Nonce: 4

## Votes

- 0xc84a0928632382E75c543ACCeD101B9525027362: DENIED, reason "R-4.3"
- 0x101d59DBB8C5e70f45D52634f23800fCa4Fbe031: APPROVED
- 0xb28eb357BFA420C17Bae3E6BfDB50094Fb5BabF2: APPROVED

## Ruling

- Outcome: secure
- Frozen: block 49444361
- Deadline: block 49928201
- Record: block 49477793, transaction 0x74257d214ed898952641a08535edf1d303bd4dc6c1a79bb0b5e63d802feb706a
- Context: the document below

<!-- DOCUMENT START -->
---

---
precedents: 0xb839c64408975c6ca5848b5e28bd95c2b82b0a28997404b4f694099726eeca1f, 0x586a2ed7fc5cbbe9043a34fb9d9d81a7f46b66740a84767c8515b782b97cf6eb, 0x6b655b88b3e9ea11cdeb69a34b375ad673ac3907670843acc8100c5974c838ed, 0x0263bd9a76793c3bdd19e1ac7718ba4646e190fc97661519bf20f41194f3e3c3, 0x844fe527e82fbf88b70e05db4c487de8143ca5d3b97e2eca24f6348c4d894434
tags: vault-redeem-request, own-shares-to-escrow, no-recipient-in-calldata, verified-proxy, verified-implementation
summary: Vault claimSharesAndRequestRedeem moving the Safe's own shares into the vault's verified pending silo, with claims to the Safe and no receiver named; secure under R-4.3.
---

Secure under R-4.3 (Charter bafkreigg6ajqdb52kb644wcxegurt7r6jlclru4mjy3lwxkbh6avm3vgre). Safe 0x3579d761025f9c755f997dfa7749775b0adebae7 on Ethereum Mainnet calls claimSharesAndRequestRedeem(uint256) on the vault 0xec5dfbb37bf296980d15086b6edfbd12d2836e50, whose implementation 0xe50554ec802375c9c3f9c087a8a7bb8c26d3dedf is Sourcify-verified. The call sends no native value and names no recipient. By the verified code, its claims go to the Safe, and it moves the Safe's shares to the vault's pending silo 0x134f24b1a4c34f18457ec5a37cf46fb9bc9215dc, a verified Silo with no function that pays out to an account, identified from the vault's creation and burn logs before block 26484870. The Safe's whole balance of the vault token came from two mints, and it called the vault before the proposal, so the silo is inside the expected target set (§ 2.4). The vault's unread storage is an absence of evidence, not determinative alone (§ 3.8). Applies 0xb839c644…, 0x586a2ed7… and 0x6b655b88… (secure); distinguishes 0x0263bd9a… and 0x844fe527… (insecure). Novel pattern, flagged for SafeDAO (§ 5.1).