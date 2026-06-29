// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.27;

import {ZetoTokenFactory} from "@lfdecentralizedtrust/zeto-contracts/contracts/factory.sol";
import {IPaladinContractRegistry_V0} from "../interfaces/IPaladinContractRegistry.sol";

contract ZetoFactory is ZetoTokenFactory, IPaladinContractRegistry_V0 {
    function deploy(
        bytes32 transactionId,
        string calldata tokenName,
        string calldata name,
        string calldata symbol,
        address initialOwner,
        bytes calldata data,
        bool isNonFungible,
        bool isEnforced
    ) external {
        require(
            !(isNonFungible && isEnforced),
            "Factory: enforced non-fungible tokens are not supported"
        );

        address instance;

        if (isNonFungible) {
            instance = deployZetoNonFungibleToken(
                name,
                symbol,
                tokenName,
                initialOwner
            );
        } else if (isEnforced) {
            instance = deployZetoEnforcedFungibleToken(
                name,
                symbol,
                tokenName,
                initialOwner
            );
        } else {
            instance = deployZetoFungibleToken(
                name,
                symbol,
                tokenName,
                initialOwner
            );
        }

        emit PaladinRegisterSmartContract_V0(transactionId, instance, data);
    }

    function deploy(
        bytes32 transactionId,
        string calldata tokenName,
        string calldata name,
        string calldata symbol,
        address initialOwner,
        bytes calldata data,
        bool isNonFungible
    ) external {
        this.deploy(
            transactionId,
            tokenName,
            name,
            symbol,
            initialOwner,
            data,
            isNonFungible,
            false
        );
    }

    function deploy(
        bytes32 transactionId,
        string calldata tokenName,
        string calldata name,
        string calldata symbol,
        address initialOwner,
        bytes calldata data
    ) external {
        this.deploy(
            transactionId,
            tokenName,
            name,
            symbol,
            initialOwner,
            data,
            false,
            false
        );
    }
}
