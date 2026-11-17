package taskengine

import "github.com/ethereum/go-ethereum/common"

// Tokens whose nonzero approvals a budget preparation may reset to zero in the
// owner's install batch, per chain. Every mainnet entry is exercised by
// TestBudgetAtomicResetAndInstall_MainnetForks with the exact production
// reset/install encoders, the real deployed token and stock MA v2 modules:
// reset, rollback when a later call in the batch fails, owner-signed install
// and a metered session spend. Sepolia USDC additionally has the live bundler
// proof.
//
// The mainnet lists are the token catalog (tokenwhitelist/*.json) less the
// tokens that failed that proof. BNB and GT on Ethereum revert on
// approve(spender, 0), so no batch can reset them. A token missing here keeps
// the existing refusal (BUDGET_RESET_UNSUPPORTED_TOKEN): the owner revokes the
// approval first. TestBudgetResetQualifiedDeploymentsRemainAuthoritative keeps
// every entry a catalog token; a new catalog token is added here only with the
// fork proof passing for it.
var budgetAutomaticResetTokens = map[int64][]common.Address{
	11155111: {common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")}, // USDC
	1: {
		common.HexToAddress("0x111111111117dC0aa78b770fA6A738034120C302"), // 1INCH
		common.HexToAddress("0x7Fc66500c84A76Ad7e9c93437bFc5Ac33E2DDaE9"), // AAVE
		common.HexToAddress("0x626E8036dEB333b408Be468F951bdB42433cBF18"), // AIOZ
		common.HexToAddress("0x4d224452801ACEd8B2F0aebE155379bb5D594381"), // APE
		common.HexToAddress("0xB50721BCf8d664c30412Cfbc6cf7a15145234ad1"), // ARB
		common.HexToAddress("0xbe0Ed4138121EcFC5c0E56B40517da27E6c5226B"), // ATH
		common.HexToAddress("0xBB0E17EF65F82Ab018d8EDd776e8DD940327B28b"), // AXS
		common.HexToAddress("0xba100000625a3754423978a60c9317c58a424e3D"), // BAL
		common.HexToAddress("0x54D2252757e1672EEaD234D27B1270728fF90581"), // BGB
		common.HexToAddress("0x1151CB3d861920e07a38e03eEAd12C32178567F6"), // Bonk
		common.HexToAddress("0xB0F70C0bD6FD87dbEb7C10dC692a2a6106817072"), // BTC.b
		common.HexToAddress("0xC669928185DbCE49d2230CC9B0979BE6DC797957"), // BTT
		common.HexToAddress("0x7712c34205737192402172409a8F7ccef8aA2AEc"), // BUIDL
		common.HexToAddress("0x152649eA73beAb28c5b49B26eb48f7EAD6d4c898"), // Cake
		common.HexToAddress("0xcbB7C0000aB88B473b1f5aFd9ef808440eed33Bf"), // cbBTC
		common.HexToAddress("0xBe9895146f7AF43049ca1c1AE358B0541Ea49704"), // cbETH
		common.HexToAddress("0xc60a9145D9e9f1152218e7da6df634b7a74aE444"), // cgETH.hashkey
		common.HexToAddress("0xE7ae30C03395D66F30A26C49c91edAe151747911"), // clBTC
		common.HexToAddress("0xE6829d9a7eE3040e1276Fa75293Bde931859e8fA"), // cmETH
		common.HexToAddress("0xc00e94Cb662C3520282E6f5717214004A7f26888"), // COMP
		common.HexToAddress("0xA0b73E1Ff0B80914AB6fe0444E65848C4C34450b"), // CRO
		common.HexToAddress("0xD533a949740bb3306d119CC777fa900bA034cd52"), // CRV
		common.HexToAddress("0xf939E0A03FB07F59A73314E73794Be0E57ac1b4E"), // crvUSD
		common.HexToAddress("0x6B175474E89094C44Da98b954EedeAC495271d0F"), // DAI
		common.HexToAddress("0xde4EE8057785A7e8e800Db58F9784845A5C2Cbd6"), // DEXE
		common.HexToAddress("0x92D6C1e31e14520e676a687F0a93788B716BEff5"), // DYDX
		common.HexToAddress("0x657e8C867D8B37dCC18fA4Caead9C45EB088C642"), // eBTC
		common.HexToAddress("0x35fA164735182de50811E8e2E824cFb9B6118ac2"), // eETH
		common.HexToAddress("0xec53bF9167f50cDEB3Ae105f56099aaaB9061F83"), // EIGEN
		common.HexToAddress("0x57e114B691Db790C35207b2e685D4A43181e6061"), // ENA
		common.HexToAddress("0xC18360217D8F7Ab5e7c516566761Ea12Ce7F9D72"), // ENS
		common.HexToAddress("0xFe0c30065B384F05761f15d0CC899D4F9F9Cc0eB"), // ETHFI
		common.HexToAddress("0xA35b1B31Ce002FBF2058D22F30f95D405200A15b"), // ETHx
		common.HexToAddress("0x1aBaEA1f7C830bD89Acc67eC4af516284b1bC33c"), // EURC
		common.HexToAddress("0x90D2af7d622ca3141efA4d8f1F24d86E5974Cc8F"), // eUSDe
		common.HexToAddress("0xbf5495Efe5DB9ce00f80364C8B423567e58d2110"), // ezETH
		common.HexToAddress("0xC96dE26018A54D51c097160568752c4E3BD6C364"), // FBTC
		common.HexToAddress("0xc5f0f7b66764F6ec8C8Dff7BA683102295E16409"), // FDUSD
		common.HexToAddress("0xaea46A60368A7bD060eec7DF8CBa43b7EF41Ad85"), // FET
		common.HexToAddress("0xcf0C122c6b73ff809C693DB761e7BaeBe62b6a2E"), // FLOKI
		common.HexToAddress("0x853d955aCEf822Db058eb8505911ED77F175b99e"), // FRAX
		common.HexToAddress("0x5E8422345238F34275888049021821E8E08CAa1f"), // frxETH
		common.HexToAddress("0xaeDf386B755465871fF874E3E37Af5976E247064"), // FTN
		common.HexToAddress("0x3432B6A60D23Ca0dFCa7761B7ab56459D9C964D0"), // FXS
		common.HexToAddress("0xd1d2Eb1B1e90B638588728b4130137D262C87cae"), // GALA
		common.HexToAddress("0x40D16FC0246aD3160Ccc09B8D0D3A2cD28aE6C2f"), // GHO
		common.HexToAddress("0xc944E90C64B2c07662A292be6244BDf05Cda44a7"), // GRT
		common.HexToAddress("0xF57e7e7C23978C3cAEC3C3548E3D615c346e79fF"), // IMX
		common.HexToAddress("0xe28b3B32B6c345A34Ff64674606124Dd5Aceca30"), // INJ
		common.HexToAddress("0x7420B4b9a0110cdC71fB720908340C03F9Bc03EC"), // JASMY
		common.HexToAddress("0xdeFA4e8a7bcBA345F687a2f1456F5Edd9CE97202"), // KNC
		common.HexToAddress("0x8236a87084f8B84306f72007F36F2618A5634494"), // LBTC
		common.HexToAddress("0x5A98FcBEA516Cf06857215779Fd812CA3beF1B32"), // LDO
		common.HexToAddress("0x2AF5D2aD76741191D15Dfe7bF6aC92d4Bd912Ca3"), // LEO
		common.HexToAddress("0x514910771AF9Ca656af840dff83E8264EcF986CA"), // LINK
		common.HexToAddress("0x8c1BEd5b9a0928467c9B1341Da1D7BD5e10b6549"), // LsETH
		common.HexToAddress("0x5f98805A4E8be255a32880FDeC7F6728C6568bA0"), // LUSD
		common.HexToAddress("0x0F5D2fB29fb7d3CFeE444a200298f468908cC942"), // MANA
		common.HexToAddress("0xd5F7838F5C461fefF7FE49ea5ebaF7728bB0ADfa"), // mETH
		common.HexToAddress("0x9f8F72aA9304c8B593d555F12eF6589cC3A579A2"), // MKR
		common.HexToAddress("0x3c3a81e81dc49A522A592e7622A7E711c06bf354"), // MNT
		common.HexToAddress("0xacA92E438df0B2401fF60dA7E4337B687a2435DA"), // mUSD
		common.HexToAddress("0x85F17Cf997934a597031b2E18a9aB6ebD4B9f6a4"), // NEAR
		common.HexToAddress("0xB62132e35a6c13ee1EE0f84dC5d40bad8d815206"), // NEXO
		common.HexToAddress("0x198d14F2Ad9CE69E76ea330B374DE4957C3F850a"), // NFT
		common.HexToAddress("0x75231F58b43240C9718Dd58B4967c5114342a86c"), // OKB
		common.HexToAddress("0xfAbA6f8e4a5E8Ab82F62fe7C39859FA577269BE3"), // ONDO
		common.HexToAddress("0xf1C9acDc66974dFB6dEcB12aA385b9cD01190E38"), // osETH
		common.HexToAddress("0x1B19C19393e2d034D8Ff31ff34c81252FcBbee92"), // OUSG
		common.HexToAddress("0x45804880De22913dAFE09f4980848ECE6EcbAf78"), // PAXG
		common.HexToAddress("0x808507121B80c02388fAd14726482e061B8da827"), // PENDLE
		common.HexToAddress("0x6982508145454Ce325dDbE47a25d4ec3d2311933"), // PEPE
		common.HexToAddress("0x455e53CBB86018Ac2B8092FdCd39d8444aFFC3F6"), // POL
		common.HexToAddress("0xF469fBD2abcd6B9de8E169d128226C0Fc90a012e"), // pumpBTC
		common.HexToAddress("0x6c3ea9036406852006290770BEdFcAbA0e23A0e8"), // PYUSD
		common.HexToAddress("0x4a220E6096B25EADb88358cb44068A3248254675"), // QNT
		common.HexToAddress("0xae78736Cd615f374D3085123A210448E74Fc6393"), // rETH
		common.HexToAddress("0x8292Bb45bf1Ee4d140127049757C2E0fF06317eD"), // RLUSD
		common.HexToAddress("0x6De037ef9aD2725EB40118Bb1702EBb27e4Aeb24"), // RNDR
		common.HexToAddress("0xD33526068D116cE69F19A9ee46F0bd304F21A51f"), // RPL
		common.HexToAddress("0xA1290d69c65A6Fe4DF752f95823fae25cB99e5A7"), // rsETH
		common.HexToAddress("0x320623b8E4fF03373931769A31Fc52A4E78B5d70"), // RSR
		common.HexToAddress("0x3845badAde8e6dFF049820680d1F14bD3903a5d0"), // SAND
		common.HexToAddress("0x83F20F44975D03b1b09e64809B757c47f942BEeA"), // sDAI
		common.HexToAddress("0xac3E018457B222d93114458476f3E3416Abbe38F"), // sfrxETH
		common.HexToAddress("0x95aD61b0a150d79219dCF64E1E6Cc01f0B64C4cE"), // SHIB
		common.HexToAddress("0x56072C95FAA701256059aa122697B133aDEd9279"), // SKY
		common.HexToAddress("0xC011a73ee8576Fb46F5E1c5751cA3B9Fe0af2a6F"), // SNX
		common.HexToAddress("0x7A56E1C57C7475CCf742a1832B028F0456652F97"), // SolvBTC
		common.HexToAddress("0xE0f63A424a4439cBE457D80E4f4b51aD25b2c56C"), // SPX
		common.HexToAddress("0xae7ab96520DE3A18E5e111B5EaAb095312D7fE84"), // stETH
		common.HexToAddress("0xAf5191B0De278C7286d6C7CC6ab6BB8A73bA2Cd6"), // STG
		common.HexToAddress("0xCa14007Eff0dB1f8135f4C25B34De49AB0d42766"), // STRK
		common.HexToAddress("0x9D39A5DE30e57443BfF2A8307A4256c8797A3497"), // sUSDe
		common.HexToAddress("0xa3931d71877C0E7a3148CB7Eb4463524FEc27fbD"), // sUSDS
		common.HexToAddress("0x643C4E15d7d62Ad0aBeC4a9BD4b001aA3Ef52d66"), // SYRUP
		common.HexToAddress("0x80ac24aA929eaF5013f6436cdA2a7ba190f5Cc0b"), // syrupUSDC
		common.HexToAddress("0x356B8d89c1e1239Cbbb9dE4815c39A1474d5BA7D"), // syrupUSDT
		common.HexToAddress("0x18084fbA666a33d37592fA2633fD49a74DD93a88"), // tBTC
		common.HexToAddress("0x467Bccd9d29f223BcE8043b84E8C8B282827790F"), // TEL
		common.HexToAddress("0xD11c452fc99cF405034ee446803b6F6c1F6d5ED8"), // tETH
		common.HexToAddress("0x667102BD3413bFEaa3Dffb48fa8288819E480a88"), // TKX
		common.HexToAddress("0x582d872A1B094FC48F5DE31D3B73F2D9bE47def1"), // TONCOIN
		common.HexToAddress("0x0000000000085d4780B73119b644AE5ecd22b376"), // TUSD
		common.HexToAddress("0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984"), // UNI
		common.HexToAddress("0x73A15FeD60Bf67631dC6cd7Bc5B6e8da8190aCF5"), // USD0
		common.HexToAddress("0x8d0D000Ee44948FC98c9B98A4FA4921476f08B0d"), // USD1
		common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), // USDC
		common.HexToAddress("0x0C10bF8FcB7Bf5412187A595ab97a3609160b5c6"), // USDD
		common.HexToAddress("0x4c9EDD5852cd905f086C759E8383e09bff1E68B3"), // USDe
		common.HexToAddress("0xe343167631d89B6Ffc58B88d6b7fB0228795491D"), // USDG
		common.HexToAddress("0xdC035D45d973E3EC169d2276DDab16f1e407384F"), // USDS
		common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"), // USDT
		common.HexToAddress("0xC139190F447e929f090Edeb554D95AbB8b18aC1C"), // USDtb
		common.HexToAddress("0xf3527ef8dE265eAa3716FB312c12847bFBA66Cef"), // USDX
		common.HexToAddress("0x96F6eF951840721AdBF46Ac996b59E0235CB985C"), // USDY
		common.HexToAddress("0x136471a34f6ef19fE571EFFC1CA711fdb8E49f2b"), // USYC
		common.HexToAddress("0x44ff8620b8cA30902395A7bD3F2407e1A091BF73"), // VIRTUAL
		common.HexToAddress("0x925206b8a707096Ed26ae47C84747fE0bb734F59"), // WBT
		common.HexToAddress("0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599"), // WBTC
		common.HexToAddress("0xCd5fE23C85820F7B72D0926FC9b05b43E359b7ee"), // weETH
		common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), // WETH
		common.HexToAddress("0x163f8C2467924be0ae7B5347228CABF260318753"), // WLD
		common.HexToAddress("0x7f39C581F595B53c5cb19bD0b3f8dA6c935E2Ca0"), // wstETH
		common.HexToAddress("0x68749665FF8D2d112Fa859AA293F07A622782F38"), // XAUt
		common.HexToAddress("0xA2cd3D43c775978A96BdBf12d733D5A1ED94fb18"), // XCN
		common.HexToAddress("0xd9D920AA40f578ab794426F5C90F6C731D159DEf"), // xSolvBTC
		common.HexToAddress("0xC71B5F631354BE6853eFe9C3Ab6b9590F8302e81"), // ZK
	},
	8453: {
		common.HexToAddress("0x63706e401c06ac8513145b7687A14804d17f814b"), // AAVE
		common.HexToAddress("0x940181a94A35A4569E4529A3CDfB74e38FD98631"), // AERO
		common.HexToAddress("0xEB466342C4d449BC9f53A865D5Cb90586f405215"), // axlUSDC
		common.HexToAddress("0x7c6b91D9Be155A6Db01f749217d76fF02A7227F2"), // BAL
		common.HexToAddress("0x532f27101965dd16442E59d40670FaF5eBB142E4"), // BRETT
		common.HexToAddress("0xcbB7C0000aB88B473b1f5aFd9ef808440eed33Bf"), // cbBTC
		common.HexToAddress("0x2Ae3F1Ec7F1F5012CFEab0185bfc7aa3cf0DEc22"), // cbETH
		common.HexToAddress("0x50c5725949A6F0c72E6C4a641F24049A917DB0Cb"), // DAI
		common.HexToAddress("0x4ed4E862860beD51a9570b96d89aF5E1B0Efefed"), // DEGEN
		common.HexToAddress("0x60a3E35Cc302bFA44Cb288Bc5a4F316Fdb1adb42"), // EURC
		common.HexToAddress("0x2416092f143378750bb29b79eD961ab195CcEea5"), // ezETH
		common.HexToAddress("0x6Bb7a212910682DCFdbd5BCBb3e28FB4E8da10Ee"), // GHO
		common.HexToAddress("0xecAc9C5F704e954931349Da37F60E39f515c11c1"), // LBTC
		common.HexToAddress("0x88Fb150BDc53A65fe94Dea0c9BA0a6dAf8C6e196"), // LINK
		common.HexToAddress("0xBAa5CC21fd487B8Fcc2F632f3F4E8D37262a0842"), // MORPHO
		common.HexToAddress("0xB6fe221Fe9EeF5aBa221c348bA20A1Bf5e73624c"), // rETH
		common.HexToAddress("0x8d58C0C60B8D6b88Fa98B291a646dB34d0F98258"), // RLUSD
		common.HexToAddress("0xaB36452DbAC151bE02b16Ca17d8919826072f64a"), // RSR
		common.HexToAddress("0x211Cc4DD073734dA055fbF44a2b4667d5E5fE5d2"), // sUSDe
		common.HexToAddress("0x5875eEE11Cf8398102FdAd704C9E96607675467a"), // sUSDS
		common.HexToAddress("0x660975730059246A68521a3e2FBD4740173100f5"), // syrupUSDC
		common.HexToAddress("0x236aa50979D5f3De3Bd1Eeb40E81137F22ab794b"), // tBTC
		common.HexToAddress("0xAC1Bd2486aAf3B5C0fc3Fd868558b082a531B2B4"), // TOSHI
		common.HexToAddress("0xc3De830EA07524a0761646a6a4e4be0e114a3C83"), // UNI
		common.HexToAddress("0xd9aAEc86B65D86f6A7B5B1b0c42FFA531710b6CA"), // USDbC
		common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"), // USDC
		common.HexToAddress("0x5d3a1Ff2b6BAb83b63cd9AD0787074081a52ef34"), // USDe
		common.HexToAddress("0x820C137fa70C8691f0e44Dc420a5e53c168921Dc"), // USDS
		common.HexToAddress("0xfde4C96c8593536E31F229EA8f37b2ADa2699bb2"), // USDT
		common.HexToAddress("0x0b3e328455c4059EEb9e3f84b5543F74E24e7E1b"), // VIRTUAL
		common.HexToAddress("0x1ceA84203673764244E05693e42E6Ace62bE9BA5"), // WBTC
		common.HexToAddress("0x04C0599Ae5A44757c0af6F9eC3b93da8976c150A"), // weETH
		common.HexToAddress("0xA88594D404727625A9437C3f886C7643872296AE"), // WELL
		common.HexToAddress("0x4200000000000000000000000000000000000006"), // WETH
		common.HexToAddress("0xEDfa23602D0EC14714057867A78d01e94176BEA0"), // wrsETH
		common.HexToAddress("0xc1CBa3fCea344f92D9239c08C0568f6F2F0ee452"), // wstETH
		common.HexToAddress("0x9EaF8C1E34F05a589EDa6BAfdF391Cf6Ad3CB239"), // YFI
	},
}

func budgetResetTokenTested(chainID int64, token common.Address) bool {
	for _, tested := range budgetAutomaticResetTokens[chainID] {
		if token == tested {
			return true
		}
	}
	return false
}
