// SPDX-License-Identifier: MIT
pragma solidity ^0.8.19;

import "./Helpers.sol";

interface IGreeter {
    function greet(string memory name) external pure returns (string memory);
}

contract Greeter is IGreeter {
    function greet(string memory name) public pure returns (string memory) {
        return Helpers.format(name);
    }
}
