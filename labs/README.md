# D7024E Lab Assignment: Creating a Decentralized Package Repository on top of a Peer-to-Peer Distributed Data Store

This document introduces the lab element in the D7024E ([Mobile and Distributed Computing Systems](https://www.ltu.se/en/education/syllabuses/course-syllabus?id=D7024E)) course at [Luleå Technical University](https://www.ltu.se/).

The assignment has two parts and a report, and you'll find the detailed specification including technical requirements in [LAB-SPEC](LAB-SPEC.md).
There are additional tips and resources that might be useful in [TIPS](TIPS.md).

This description is a work in progress. Please report any errors you find.


## Introduction

The objective of this assignment is to build a distributed and decentralized package repository (similar to e.g. [Maven Central](https://mvnrepository.com/repos/central) or [npm](https://www.npmjs.com/) or [crates.io](https://crates.io/) but decentralized) on top of a [Distributed Data Store (DDS)](https://en.wikipedia.org/wiki/Distributed_data_store) by implementing the [Kademlia](https://en.wikipedia.org/wiki/Kademlia) [Distributed Hash Table (DHT)](https://en.wikipedia.org/wiki/Distributed_hash_table).

In contrast to a traditional database, a DDS stores its *data objects* (or *values*, *blobs*) on many different computers rather than only one.
These computers together make up a single *storage network*, which internally keeps track of what objects are stored and which *nodes* keep copies of them.
A *node*, or *peer*, is simply a participant in a peer-to-peer network.
The system you are to create will be able to store and locate data objects by their [*hashes*](https://en.wikipedia.org/wiki/Hash_function) in such a storage network, which is formed by many running instances of your system.


## Motivation

The primary intention of the lab is to help you understand, both practically and theoretically, how modern distributed applications are built and some design challenges faced while constructing them.
We want you to see for yourself how these are more complicated and less efficient than their traditional client-server counterparts, but also how they facilitate scalability and fault-tolerance.
You will assess these benefits by running some experiments on your implementation.

We also want to introduce you to some of the tools and technologies that are commonly employed to build and manage distributed systems, such as containerization.
